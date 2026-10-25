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
export type DiagramIR = FlowIR;

/** The largest diagram drawn editorially. The prompt asks for at most 12
 * nodes; past this the layout stops reading as composed, and the renderer's
 * own drawing is no worse than a crowded one of ours. */
const maxNodes = 16;
const maxEdges = 24;

export function fits(ir: DiagramIR): boolean {
  return ir.nodes.length <= maxNodes && ir.edges.length <= maxEdges;
}

/** toIR reads the db of a parsed diagram, or returns null for a kind or a
 * construct the drawing does not cover. */
export function toIR(type: string, db: unknown): DiagramIR | null {
  try {
    if (type === "flowchart-v2" || type === "flowchart") return flowIR(db);
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
