/**
 * What a diagram says, read out of the mermaid renderer's own parse, in the
 * shape rongo's drawing (diagramDraw.ts) takes.
 *
 * The model writes mermaid and that stays the stored form; only the picture
 * is rongo's. Reading the renderer's parse rather than the text means the
 * fence is accepted exactly as far as it was before: what mermaid.parse
 * takes, this reads, and there is no second grammar to drift from the first.
 *
 * This is the one file that knows the renderer's internals (the db behind
 * getDiagramFromText), so a renderer bump that moves them breaks here and in
 * corpus.test.ts, never on screen. Like parseDiagram it degrades per element:
 * an unknown shape draws as a step, an edge to an undeclared node is dropped.
 * What it cannot draw honestly at all is null, and the renderer draws that
 * as it always did.
 */

export type FlowShape = "pill" | "step" | "decision";
export type FlowNodeIR = {
  id: string;
  label: string;
  shape: FlowShape;
  zone?: string;
  /** Tagged `:::focus` by the model: the part the question is about. */
  focus?: boolean;
};
export type FlowEdgeIR = {
  from: string;
  to: string;
  label: string;
  dashed: boolean;
  /** Which end carries the arrowhead. */
  head: "end" | "both" | "none";
};
export type FlowIR = {
  kind: "flow";
  direction: "TB" | "LR";
  nodes: FlowNodeIR[];
  edges: FlowEdgeIR[];
  zones: { id: string; title: string }[];
};
/** How a message is drawn: its line, and what its arrow ends in. A dashed
 * line is a reply, never a boundary, so it takes no colour. */
export type SeqLine = "solid" | "dashed";
export type SeqHead = "filled" | "open" | "none" | "cross" | "both";
export type SeqRow =
  | {
      type: "message";
      from: string;
      to: string;
      label: string;
      line: SeqLine;
      head: SeqHead;
    }
  | {
      type: "note";
      /** The lifelines it sits over, or the one it sits beside. */
      from: string;
      to: string;
      side: "over" | "left" | "right";
      label: string;
    }
  /** A frame opens (`alt`, `loop`, `par` ...), is split by a divider
   * (`else`, `and`, `option`) and closes at `end`. */
  | { type: "frame"; keyword: string; label: string }
  | { type: "divider"; keyword: string; label: string }
  | { type: "end" };
export type SequenceIR = {
  kind: "sequence";
  actors: { id: string; label: string }[];
  /** Top to bottom, in the order the source wrote them. */
  rows: SeqRow[];
};
export type DiagramIR = FlowIR | SequenceIR;

/** The largest diagram drawn editorially. The prompt asks for at most 12
 * nodes; past this the layout stops reading as composed, and the renderer's
 * own drawing is no worse than a crowded one of ours. */
const maxNodes = 16;
const maxEdges = 24;
/** The prompt asks for at most 5 participants and 12 messages; the same
 * slack over it as a flow gets. Wider than this a sequence is too small to
 * read inline in either drawing, and the renderer's is the wider one. */
const maxActors = 8;
const maxRows = 20;

export function fits(ir: DiagramIR): boolean {
  if (ir.kind === "sequence") {
    const rows = ir.rows.filter(
      (r) => r.type === "message" || r.type === "note",
    );
    return ir.actors.length <= maxActors && rows.length <= maxRows;
  }
  return ir.nodes.length <= maxNodes && ir.edges.length <= maxEdges;
}

/** toIR reads the db of a parsed diagram, or returns null for a kind or a
 * construct the drawing does not cover. */
export function toIR(type: string, db: unknown): DiagramIR | null {
  try {
    if (type === "flowchart-v2" || type === "flowchart") return flowIR(db);
    if (type === "sequence") return sequenceIR(db);
  } catch {
    // A db missing an accessor this reads is a renderer that moved on; the
    // renderer then draws the source itself.
  }
  return null;
}

type FlowDb = {
  getVertices(): Map<
    string,
    { id: string; text?: string; type?: string; classes?: string[] }
  >;
  getEdges(): {
    start: string;
    end: string;
    type?: string;
    text?: string;
    stroke?: string;
  }[];
  getSubGraphs(): { id: string; title?: string; nodes: string[] }[];
  getDirection(): string | undefined;
};

function isFlowDb(db: unknown): db is FlowDb {
  if (typeof db !== "object" || db === null) return false;
  const d = db as Record<string, unknown>;
  return ["getVertices", "getEdges", "getSubGraphs", "getDirection"].every(
    (k) => typeof d[k] === "function",
  );
}

/** Pills are where a flow starts or stops; a diamond is a decision; every
 * other mermaid shape is a step, because shape carries type and rongo's
 * flows have only those three. */
const pills = new Set(["stadium", "circle", "doublecircle", "ellipse"]);

function flowIR(db: unknown): FlowIR | null {
  if (!isFlowDb(db)) return null;
  // Bottom-up and right-to-left are orders the layouts do not draw; read as
  // their mirror, a stack would come out upside down.
  const dir = db.getDirection() ?? "TB";
  if (dir === "BT" || dir === "RL") return null;
  const subgraphs = db.getSubGraphs();
  const zoneIds = new Set(subgraphs.map((s) => s.id));
  const zoneOf = new Map<string, string>();
  for (const s of subgraphs) {
    for (const n of s.nodes) {
      // A subgraph listing another one nests, and nesting is a picture this
      // does not draw.
      if (zoneIds.has(n)) return null;
      if (!zoneOf.has(n)) zoneOf.set(n, s.id);
    }
  }
  const nodes: FlowNodeIR[] = [];
  const known = new Set<string>();
  for (const v of db.getVertices().values()) {
    if (typeof v.id !== "string" || v.id === "" || known.has(v.id)) continue;
    known.add(v.id);
    const node: FlowNodeIR = {
      id: v.id,
      label: plain(v.text ?? "") || v.id,
      shape:
        v.type === "diamond"
          ? "decision"
          : pills.has(v.type ?? "")
            ? "pill"
            : "step",
    };
    const zone = zoneOf.get(v.id);
    if (zone !== undefined) node.zone = zone;
    if (v.classes?.includes("focus")) node.focus = true;
    nodes.push(node);
  }
  const edges: FlowEdgeIR[] = [];
  for (const e of db.getEdges()) {
    // An edge to a subgraph lands on its frame, which the layout cannot
    // route to as it routes to a node.
    if (zoneIds.has(e.start) || zoneIds.has(e.end)) return null;
    if (!known.has(e.start) || !known.has(e.end)) continue;
    // `~~~` is a layout hint, not a connection.
    if (e.stroke === "invisible") continue;
    edges.push({
      from: e.start,
      to: e.end,
      label: plain(e.text ?? ""),
      dashed: e.stroke === "dotted",
      head: headOf(e.type ?? ""),
    });
  }
  if (nodes.length === 0) return null;
  return {
    kind: "flow",
    direction: dir === "LR" ? "LR" : "TB",
    nodes,
    edges,
    // A subgraph holding no node is no zone: drawn, it is an empty frame,
    // and counted, it would keep the flow from the layouts built for it.
    zones: subgraphs
      .filter((s) => nodes.some((n) => n.zone === s.id))
      .map((s) => ({ id: s.id, title: plain(s.title ?? "") })),
  };
}

type SequenceDb = {
  getActors(): Map<string, { name?: string; description?: string }>;
  getMessages(): {
    from?: string;
    to?: string;
    type?: number;
    // An object on an autonumber record ({ visible, start, step }).
    message?: unknown;
    placement?: number;
  }[];
  getBoxes(): unknown[];
  getCreatedActors(): Map<string, unknown>;
  getDestroyedActors(): Map<string, unknown>;
  getDiagramTitle?(): string;
};

function isSequenceDb(db: unknown): db is SequenceDb {
  if (typeof db !== "object" || db === null) return false;
  const d = db as Record<string, unknown>;
  return [
    "getActors",
    "getMessages",
    "getBoxes",
    "getCreatedActors",
    "getDestroyedActors",
  ].every((k) => typeof d[k] === "function");
}

/** The renderer's line types (its LINETYPE table), the ones drawn. */
const arrows: Record<number, [SeqLine, SeqHead]> = {
  0: ["solid", "filled"],
  1: ["dashed", "filled"],
  3: ["solid", "cross"],
  4: ["dashed", "cross"],
  5: ["solid", "none"],
  6: ["dashed", "none"],
  24: ["solid", "open"],
  25: ["dashed", "open"],
  33: ["solid", "both"],
  34: ["dashed", "both"],
};
const note = 2;
const frames: Record<number, string> = {
  10: "loop",
  12: "alt",
  15: "opt",
  19: "par",
  27: "critical",
  30: "break",
};
const dividers: Record<number, string> = {
  13: "else",
  20: "and",
  28: "option",
};
const ends = new Set([11, 14, 16, 21, 29, 31]);
/** How deep frames may sit inside one another. */
const maxDepth = 3;
/** activate and deactivate: a bar on the lifeline, which this does not
 * draw, and nothing else changes. */
const ignored = new Set([17, 18]);
/** autonumber: numbering is declined, `autonumber off` is nothing. The
 * parse sets no flag for it; the record says which. */
const autonumber = 26;
const sides = ["left", "right", "over"] as const;

function sequenceIR(db: unknown): SequenceIR | null {
  if (!isSequenceDb(db)) return null;
  // Groups of participants, participants arriving or leaving mid-way and a
  // title above the picture are things this does not draw; the renderer
  // does.
  if (
    (db.getDiagramTitle?.() ?? "") !== "" ||
    db.getBoxes().length > 0 ||
    db.getCreatedActors().size > 0 ||
    db.getDestroyedActors().size > 0
  )
    return null;
  const actors: SequenceIR["actors"] = [];
  for (const [id, a] of db.getActors()) {
    if (typeof id !== "string" || id === "") continue;
    actors.push({ id, label: plain(a.description ?? a.name ?? "") || id });
  }
  if (actors.length === 0) return null;
  const known = new Set(actors.map((a) => a.id));
  const rows: SeqRow[] = [];
  let depth = 0;
  for (const m of db.getMessages()) {
    const t = m.type ?? -1;
    const label = plain(typeof m.message === "string" ? m.message : "");
    if (t in arrows) {
      if (!known.has(m.from ?? "") || !known.has(m.to ?? "")) continue;
      const [line, head] = arrows[t];
      rows.push({
        type: "message",
        from: m.from!,
        to: m.to!,
        label,
        line,
        head,
      });
    } else if (t === note) {
      if (!known.has(m.from ?? "") || !known.has(m.to ?? "")) continue;
      const side = sides[m.placement ?? 2] ?? "over";
      rows.push({ type: "note", from: m.from!, to: m.to!, side, label });
    } else if (t in frames) {
      // A frame inside a frame is drawn inset in it. Deeper than a few the
      // insets eat the columns, and the renderer's drawing is no worse.
      if (++depth > maxDepth) return null;
      rows.push({ type: "frame", keyword: frames[t], label });
    } else if (t in dividers) {
      if (depth === 0) return null;
      rows.push({ type: "divider", keyword: dividers[t], label });
    } else if (ends.has(t)) {
      if (depth === 0) return null;
      depth--;
      rows.push({ type: "end" });
    } else if (t === autonumber) {
      const on = (m.message as { visible?: unknown } | undefined)?.visible;
      if (on !== false) return null;
    } else if (!ignored.has(t)) {
      // rect, par_over, and whatever a later renderer adds.
      return null;
    }
  }
  if (depth !== 0) return null;
  return { kind: "sequence", actors, rows };
}

function headOf(type: string): FlowEdgeIR["head"] {
  if (type.startsWith("double_")) return "both";
  if (type === "arrow_open") return "none";
  return "end";
}

/** plain is a label as the reader should see it, as mermaid's own drawing
 * showed it: a line break is a line break, other markup is dropped, the
 * entities the parse left in are decoded, a markdown label loses its
 * backticks and emphasis, and space is collapsed.
 *
 * The parse leaves two layers of escaping. It sanitises node and subgraph
 * text into HTML (`&` arrives as `&amp;`), and it parks the source's own
 * entity codes (`#quot;`, `#35;`) behind private placeholder characters
 * that its drawing turns back into entities on the way out. Drawn as text
 * with neither undone, `Tom & Jerry` read `Tom &amp;amp; Jerry` and a quote
 * read as the placeholder. */
export function plain(s: string): string {
  const html = s
    .replace(/ﬂ°°/g, "&#")
    .replace(/ﬂ°/g, "&")
    .replace(/¶ß/g, ";")
    .replace(/<br\s*\/?>/gi, "\n")
    .replace(/<[^>]*>/g, "");
  return markdown(decode(html))
    .split("\n")
    .map((l) => l.replace(/\s+/g, " ").trim())
    .filter((l) => l !== "")
    .join("\n");
}

/** decode turns HTML entities into the characters they name. Through a
 * textarea, whose content is never parsed as elements, so nothing in a
 * label can become markup on the way. */
function decode(s: string): string {
  if (!s.includes("&") || typeof document === "undefined") return s;
  const t = document.createElement("textarea");
  t.innerHTML = s;
  return t.value;
}

/** markdown drops the markup of a backtick-quoted markdown label, which
 * mermaid draws formatted and this draws as plain text. */
function markdown(s: string): string {
  const m = /^\s*`([\s\S]*)`\s*$/.exec(s);
  if (!m) return s;
  return m[1]
    .replace(/\*\*(.+?)\*\*/g, "$1")
    .replace(/__(.+?)__/g, "$1")
    .replace(/\*(.+?)\*/g, "$1")
    .replace(/(^|\s)_(.+?)_(?=\s|$)/g, "$1$2");
}
