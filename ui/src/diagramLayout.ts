import type { ElkExtendedEdge, ElkNode, ElkPort } from "elkjs/lib/elk-api";
import type { FlowEdgeIR, FlowIR, FlowNodeIR } from "./diagramIR";
import { ramp, textWidth, wrap, type Face } from "./diagramTheme";

/**
 * Where everything in a drawing goes, composed the way the reference design
 * composes a flow: the main path is one straight column of equal boxes, a
 * branch leaves it to the right at the height of its decision and ends in an
 * outcome there, and an edge that skips back or ahead along the path runs in
 * a lane on the left. Every arrow is straight or bent at right angles, every
 * label sits beside its line, never on it.
 *
 * Three layouts, tried in order: the mapping (two columns, layoutMapping),
 * the spine (layoutSpine), and for any other shape ELK's layered layout with
 * orthogonal routing, sized and drawn the same way.
 */

export type Point = { x: number; y: number };
/** step: a box on the path. pill: where the flow starts or stops, the bold
 * box. decision: a flat diamond. outcome: a branch's end, off the path.
 * start and stop: a state diagram's `[*]`, a dot and a ringed dot. */
export type NodeKind =
  "step" | "pill" | "decision" | "outcome" | "start" | "stop";

/** dotted says a node is drawn as a circle, which an edge meets at its
 * outline, not at a side of its box. */
export const dotted = (k: NodeKind) => k === "start" || k === "stop";
export type NodeBox = {
  id: string;
  kind: NodeKind;
  lines: string[];
  face: Face;
  size: number;
  bold: boolean;
  /** Drawn in the accent: the part the question is about. */
  focus: boolean;
  x: number;
  y: number;
  w: number;
  h: number;
};
export type LabelBox = {
  lines: string[];
  x: number;
  y: number;
  w: number;
  h: number;
  /** Set along a vertical lane, read bottom to top. */
  vertical?: boolean;
};
export type EdgeRoute = {
  from: string;
  to: string;
  points: Point[];
  dashed: boolean;
  /** A dotted arrow in the source: async, optional, across a boundary. The
   * reference's amber. A loop back up the path is dashed but not this. */
  warn: boolean;
  head: FlowEdgeIR["head"];
  label?: LabelBox;
};
export type ZoneBox = {
  id: string;
  title: string;
  x: number;
  y: number;
  w: number;
  h: number;
};
export type FlowLayout = {
  width: number;
  height: number;
  nodes: NodeBox[];
  edges: EdgeRoute[];
  zones: ZoneBox[];
};

export type Elk = { layout(graph: ElkNode): Promise<ElkNode> };

/** Text to frame, each side. */
const padX = 16;
/** Space around the drawing, inside the canvas. */
const margin = 24;
/** Vertical space between two boxes on the path; the arrow between them. */
const stepGap = 24;
/** Clear space between a label and its line. */
const labelGap = 8;
const labelLine = 15;
/** The widest an arrow label runs before it wraps. */
const labelMax = 180;
/** Distance between two parallel lanes. */
const lane = 12;

const up4 = (v: number) => Math.ceil(v / 4) * 4;
const up8 = (v: number) => Math.ceil(v / 8) * 8;

// ---- what a node looks like ----

/** looksLikeCode says a label names something in the code — a call, a
 * member, a path, a camel-cased name — which the reference sets in the mono. */
export function looksLikeCode(label: string): boolean {
  return label
    .split(/\s+/)
    .some((t) => /\w\(|\(\)|[a-z][A-Z]|\w[._/]\w|\w\[/.test(t));
}

type Style = { face: Face; size: number; bold: boolean };

function styleOf(n: FlowNodeIR): Style {
  if (looksLikeCode(n.label))
    return { face: "mono", size: ramp.code, bold: false };
  if (n.shape === "pill") return { face: "sans", size: ramp.bold, bold: true };
  return { face: "sans", size: ramp.text, bold: false };
}

const width1 = (n: FlowNodeIR) => {
  const s = styleOf(n);
  return Math.max(
    ...n.label.split("\n").map((l) => textWidth(l, s.size, s.face)),
  );
};

/** longestWord is the widest run a label cannot be wrapped inside. */
const longestWord = (n: FlowNodeIR) => {
  const s = styleOf(n);
  return Math.max(
    ...n.label.split(/\s+/).map((t) => textWidth(t, s.size, s.face)),
  );
};

/** boxWidth is the one width a set of boxes shares: the longest label on one
 * line, within bounds; past the upper bound labels wrap. Never narrower than
 * the longest word, which wrapping cannot shorten: a long identifier widens
 * every box rather than running out of its own. */
function boxWidth(nodes: FlowNodeIR[], min: number, max: number): number {
  const w = Math.max(0, ...nodes.map(width1));
  const word = Math.max(0, ...nodes.map(longestWord));
  return Math.max(
    Math.min(max, Math.max(min, up8(w + 2 * padX))),
    up8(word + 2 * padX),
  );
}

function lay(n: FlowNodeIR, kind: NodeKind, w: number): NodeBox {
  const s = styleOf(n);
  const lines = wrap(n.label, w - 2 * padX, s.size, s.face);
  const base = kind === "outcome" ? 36 : 40;
  return {
    id: n.id,
    kind,
    lines,
    ...s,
    focus: n.focus ?? false,
    x: 0,
    y: 0,
    w,
    h: up4(base + (lines.length - 1) * ramp.lineHeight),
  };
}

/** shaped is a node's box at the width its layout gives boxes: a dot for a
 * start or a stop, a small diamond for a decision with no text (a state
 * diagram's choice), a diamond, or a box of the given kind. */
function shaped(n: FlowNodeIR, kind: NodeKind, w: number): NodeBox {
  if (n.shape === "start" || n.shape === "stop") {
    const d = n.shape === "start" ? 16 : 20;
    return { ...lay(n, n.shape, d), lines: [], w: d, h: d };
  }
  if (n.shape === "decision")
    return n.label === ""
      ? { ...lay(n, "decision", 28), lines: [], w: 28, h: 28 }
      : diamond(n, w);
  return lay(n, kind, w);
}

/** diamond sizes a decision: as wide as the path's boxes less a step each
 * side, its label wrapped to fit, taller rather than wider. A label tw by th
 * fits inside a diamond w by h when tw/w + th/h <= 1, so each height has a
 * text width it allows; the first height whose wrap fits is the diamond.
 * Only a word too long for any wrap widens it. */
function diamond(n: FlowNodeIR, pathW: number): NodeBox {
  const s = styleOf(n);
  const w0 = pathW - 20;
  // A label's own line breaks are lines it will take whatever the width.
  const kMax = Math.max(4, n.label.split("\n").length);
  const hMax = up4(96 + (kMax - 4) * ramp.lineHeight * 2);
  const fits = (w: number, h: number) => {
    for (let k = 1; k <= kMax; k++) {
      const th = k * ramp.lineHeight;
      if (th >= h) return null;
      const room = w * (1 - th / h) - padX;
      const lines = wrap(n.label, room, s.size, s.face);
      if (
        lines.length <= k &&
        lines.every((l) => textWidth(l, s.size, s.face) <= room)
      )
        return lines;
    }
    return null;
  };
  const box = (lines: string[], w: number, h: number): NodeBox => ({
    id: n.id,
    kind: "decision",
    lines,
    ...s,
    focus: n.focus ?? false,
    x: 0,
    y: 0,
    w,
    h,
  });
  const wMax = w0 + 480;
  for (let w = w0; w <= wMax; w += 16) {
    for (let h = 52; h <= hMax; h += 4) {
      const lines = fits(w, h);
      if (lines) return box(lines, w, h);
    }
  }
  // Nothing fits even that wide: the widest, tallest diamond, its label
  // wrapped to the middle, rather than no answer at all.
  return box(wrap(n.label, wMax / 2, s.size, s.face), wMax, hMax);
}

export function labelBox(text: string): {
  lines: string[];
  w: number;
  h: number;
} {
  const lines = wrap(text, labelMax, ramp.label, "mono");
  const tw = Math.max(...lines.map((l) => textWidth(l, ramp.label, "mono")));
  return { lines, w: Math.ceil(tw), h: lines.length * labelLine };
}

// ---- dispatch ----

/** layoutFlow places a flowchart. */
export async function layoutFlow(ir: FlowIR, elk: Elk): Promise<FlowLayout> {
  if (isMapping(ir)) return layoutMapping(ir);
  return layoutSpine(ir) ?? layoutElk(ir, elk);
}

// ---- the spine ----

type Indexed = { e: FlowEdgeIR; i: number };

/** layoutSpine places a top-down flow whose shape is a main path with
 * branches off it, or returns null for any other shape.
 *
 * The path is the longest one from the start, ties to the edge written
 * first. Every node off it must hang from exactly one node on it, as a
 * single outcome or a short chain to its right; an edge between two nodes
 * on the path that are not neighbours runs in a lane on the left. Anything
 * else — a branch rejoining the path, two branches from one node, a second
 * start — is not this shape, and ELK draws it. */
export function layoutSpine(ir: FlowIR): FlowLayout | null {
  if (ir.direction !== "TB" || ir.zones.length > 0) return null;
  if (ir.edges.some((e) => e.from === e.to)) return null;
  const outs = new Map<string, Indexed[]>();
  const ins = new Map<string, Indexed[]>();
  for (const n of ir.nodes) {
    outs.set(n.id, []);
    ins.set(n.id, []);
  }
  ir.edges.forEach((e, i) => {
    outs.get(e.from)!.push({ e, i });
    ins.get(e.to)!.push({ e, i });
  });
  const start =
    ir.nodes.find((n) => ins.get(n.id)!.length === 0) ?? ir.nodes[0];

  // Longest path from the start, never through a node already on the way.
  // Every simple path is walked: no memo, since a cycle makes a node's
  // longest path depend on how it was reached. fits() caps a drawing at 16
  // nodes and 24 edges, which keeps the walk small.
  const longest = (id: string, stack: Set<string>): string[] => {
    stack.add(id);
    let best: string[] = [];
    for (const { e } of outs.get(id)!) {
      if (stack.has(e.to)) continue;
      const p = longest(e.to, stack);
      if (p.length > best.length) best = p;
    }
    stack.delete(id);
    return [id, ...best];
  };
  const spine = longest(start.id, new Set());
  const pos = new Map(spine.map((id, k) => [id, k]));

  // Branches: at most one per node on the path, each a chain of nodes with
  // one way in and at most one way on.
  const chains = new Map<string, { edges: number[]; nodes: string[] }>();
  const placed = new Set(spine);
  const laneEdges: number[] = [];
  const nextEdge = new Map<string, number>();
  for (const id of spine) {
    for (const { e, i } of outs.get(id)!) {
      const k = pos.get(id)!;
      if (pos.has(e.to)) {
        if (pos.get(e.to) === k + 1 && !nextEdge.has(id)) nextEdge.set(id, i);
        else laneEdges.push(i);
        continue;
      }
      if (chains.has(id)) return null;
      const chain = { edges: [i], nodes: [e.to] };
      let cur = e.to;
      for (;;) {
        if (placed.has(cur) || ins.get(cur)!.length !== 1) return null;
        placed.add(cur);
        const o = outs.get(cur)!;
        if (o.length === 0) break;
        if (o.length > 1 || pos.has(o[0].e.to)) return null;
        chain.edges.push(o[0].i);
        chain.nodes.push(o[0].e.to);
        cur = o[0].e.to;
      }
      chains.set(id, chain);
    }
  }
  if (placed.size !== ir.nodes.length) return null;

  // A node's left side takes the lanes: a diamond has one corner there, a
  // box room for two.
  const leftUse = new Map<string, number>();
  for (const i of laneEdges) {
    for (const id of [ir.edges[i].from, ir.edges[i].to])
      leftUse.set(id, (leftUse.get(id) ?? 0) + 1);
  }
  const byId = new Map(ir.nodes.map((n) => [n.id, n]));
  for (const [id, k] of leftUse) {
    const one = ["decision", "start", "stop"].includes(byId.get(id)!.shape);
    if (k > (one ? 1 : 2)) return null;
  }

  // Sizes: the path's boxes share a width, the outcomes theirs.
  const pathNodes = spine.map((id) => byId.get(id)!);
  const pathW = boxWidth(
    pathNodes.filter((n) => n.shape !== "decision"),
    200,
    240,
  );
  const sideNodes = [...chains.values()].flatMap((c) =>
    c.nodes.map((id) => byId.get(id)!),
  );
  const sideW = boxWidth(sideNodes, 200, 280);
  const boxes = new Map<string, NodeBox>();
  for (const n of pathNodes)
    boxes.set(n.id, shaped(n, n.shape === "pill" ? "pill" : "step", pathW));
  for (const n of sideNodes) boxes.set(n.id, shaped(n, "outcome", sideW));

  // Down the path. A row is as tall as its tallest box; a branch is centred
  // on its node. The gap below a box holds the label of the arrow out of it,
  // and the space above a row the labels over its branch's arrows, however
  // many lines they wrapped to.
  const labels = ir.edges.map((e) =>
    e.label !== "" ? labelBox(e.label) : null,
  );
  const cx = 0;
  let y = 0;
  let prevBottom = -Infinity;
  const rowY = new Map<string, number>();
  for (const id of spine) {
    const b = boxes.get(id)!;
    const chain = chains.get(id);
    const rowH = Math.max(
      b.h,
      ...(chain?.nodes.map((n) => boxes.get(n)!.h) ?? []),
    );
    const above = Math.max(
      0,
      ...(chain?.edges.map((i) => (labels[i] ? labels[i]!.h + labelGap : 0)) ??
        []),
    );
    y = Math.max(y, prevBottom + above + 8 - rowH / 2);
    const mid = y + rowH / 2;
    rowY.set(id, mid);
    b.x = cx - b.w / 2;
    b.y = mid - b.h / 2;
    for (const n of chain?.nodes ?? [])
      boxes.get(n)!.y = mid - boxes.get(n)!.h / 2;
    prevBottom = mid + rowH / 2;
    const down = nextEdge.get(id);
    const l = down === undefined ? null : labels[down];
    y += rowH + Math.max(stepGap, l ? up4(l.h + 12) : 0);
  }
  const pathRight = Math.max(
    ...spine.map((id) => boxes.get(id)!.x + boxes.get(id)!.w),
  );
  const pathLeft = Math.min(...spine.map((id) => boxes.get(id)!.x));

  // To the right: one column per position in a chain, far enough out for the
  // widest label on the arrows into it.
  const depth = Math.max(0, ...[...chains.values()].map((c) => c.nodes.length));
  const colX: number[] = [];
  let x = pathRight;
  for (let k = 0; k < depth; k++) {
    const w = Math.max(
      0,
      ...[...chains.values()]
        .map((c) => labels[c.edges[k]]?.w ?? 0)
        .filter((v) => v > 0),
    );
    x += Math.max(k === 0 ? 96 : 48, up8(w + 48));
    colX.push(x);
    x += sideW;
  }
  for (const c of chains.values())
    c.nodes.forEach((n, k) => (boxes.get(n)!.x = colX[k]));

  const routes: EdgeRoute[] = [];
  const route = (i: number, points: Point[], label?: LabelBox) => {
    const e = ir.edges[i];
    routes[i] = {
      from: e.from,
      to: e.to,
      points,
      dashed: e.dashed,
      warn: e.dashed,
      head: e.head,
      ...(label ? { label } : {}),
    };
  };
  for (const [id, i] of nextEdge) {
    const a = boxes.get(id)!;
    const b = boxes.get(ir.edges[i].to)!;
    const l = labels[i];
    route(
      i,
      [
        { x: cx, y: a.y + a.h },
        { x: cx, y: b.y },
      ],
      l
        ? { ...l, x: cx + labelGap, y: (a.y + a.h + b.y) / 2 - l.h / 2 }
        : undefined,
    );
  }
  for (const [id, c] of chains) {
    const ys = rowY.get(id)!;
    let from = boxes.get(id)!;
    c.edges.forEach((i, k) => {
      const to = boxes.get(c.nodes[k])!;
      const x0 = from.x + from.w;
      const l = labels[i];
      route(
        i,
        [
          { x: x0, y: ys },
          { x: to.x, y: ys },
        ],
        l
          ? { ...l, x: (x0 + to.x) / 2 - l.w / 2, y: ys - labelGap - l.h }
          : undefined,
      );
      from = to;
    });
  }

  // To the left: lanes, the shortest span innermost, so lanes nest rather
  // than cross. A labelled lane keeps room on its left for the label.
  const spans = laneEdges
    .map((i) => {
      const a = rowY.get(ir.edges[i].from)!;
      const b = rowY.get(ir.edges[i].to)!;
      return { i, lo: Math.min(a, b), hi: Math.max(a, b) };
    })
    .sort((p, q) => p.hi - p.lo - (q.hi - q.lo) || p.i - q.i);
  const used = new Map<string, number>();
  let lx = pathLeft - 32;
  for (const s of spans) {
    const e = ir.edges[s.i];
    const a = boxes.get(e.from)!;
    const b = boxes.get(e.to)!;
    const port = (n: NodeBox) => {
      const k = used.get(n.id) ?? 0;
      used.set(n.id, k + 1);
      const mid = rowY.get(n.id)!;
      if (
        n.kind === "decision" ||
        dotted(n.kind) ||
        (leftUse.get(n.id) ?? 0) < 2
      )
        return mid;
      return mid + (k === 0 ? -8 : 8);
    };
    const leftOf = (n: NodeBox) => n.x;
    const y0 = port(a);
    const y1 = port(b);
    const l = labels[s.i];
    let label: LabelBox | undefined;
    if (l) {
      // Along the lane when it is long enough to carry the label, else level
      // with where the edge leaves.
      const len = Math.abs(y1 - y0);
      label =
        l.lines.length === 1 && len >= l.w + 16
          ? {
              ...l,
              w: l.h,
              h: l.w,
              x: lx - labelGap - l.h,
              y: (y0 + y1) / 2 - l.w / 2,
              vertical: true,
            }
          : { ...l, x: lx - labelGap - l.w, y: y0 - l.h / 2 };
    }
    route(
      s.i,
      [
        { x: leftOf(a), y: y0 },
        { x: lx, y: y0 },
        { x: lx, y: y1 },
        { x: leftOf(b), y: y1 },
      ],
      label,
    );
    // Back up the path is a loop, drawn dashed as the reference draws its retry.
    if (pos.get(e.to)! < pos.get(e.from)!) routes[s.i].dashed = true;
    lx -= lane + (label ? label.w + labelGap : 0);
  }

  const nodes = ir.nodes.map((n) => boxes.get(n.id)!);
  return frame(nodes, routes, []);
}

/** frame moves a drawing so its leftmost and topmost marks sit margin in,
 * and sizes the canvas around it. */
export function frame(
  nodes: NodeBox[],
  edges: EdgeRoute[],
  zones: ZoneBox[],
): FlowLayout {
  const rects = [
    ...nodes,
    ...zones,
    ...edges.flatMap((e) => (e.label ? [e.label] : [])),
  ];
  const pts = edges.flatMap((e) => e.points);
  const minX = Math.min(...rects.map((r) => r.x), ...pts.map((p) => p.x));
  const minY = Math.min(...rects.map((r) => r.y), ...pts.map((p) => p.y));
  const maxX = Math.max(...rects.map((r) => r.x + r.w), ...pts.map((p) => p.x));
  const maxY = Math.max(...rects.map((r) => r.y + r.h), ...pts.map((p) => p.y));
  const dx = margin - minX;
  const dy = margin - minY;
  for (const r of rects) {
    r.x += dx;
    r.y += dy;
  }
  for (const p of pts) {
    p.x += dx;
    p.y += dy;
  }
  return {
    width: Math.ceil(maxX - minX + 2 * margin),
    height: Math.ceil(maxY - minY + 2 * margin),
    nodes,
    edges,
    zones,
  };
}

// ---- the mapping ----

/** isMapping says a flowchart is the prompt's trigger-to-target mapping: left
 * to right, two subgraphs holding every node, every edge from the first to
 * the second. */
export function isMapping(ir: FlowIR): boolean {
  if (ir.direction !== "LR" || ir.zones.length !== 2) return false;
  const [a, b] = ir.zones.map((z) => z.id);
  const zone = new Map(ir.nodes.map((n) => [n.id, n.zone]));
  return (
    ir.nodes.every((n) => n.zone === a || n.zone === b) &&
    ir.edges.length > 0 &&
    ir.edges.every((e) => zone.get(e.from) === a && zone.get(e.to) === b)
  );
}

/** Vertical space between the stacked boxes of a column. */
const rowGap = 20;
/** A zone's inner margin; the top holds its title. */
const zonePad = { top: 44, side: 20, bottom: 20 };

/** titleRoom is the width a zone needs for its title: the bold title, set
 * 18px in, and as much again on the right. */
export function titleRoom(title: string): number {
  return up8(textWidth(title, ramp.bold, "sans") + 36);
}

/** layoutMapping places a trigger-to-target mapping as two frames side by
 * side, by hand rather than through ELK: the triggers stay in the order the
 * model wrote them, which a layered layout re-sorts for its own reasons.
 *
 * Laid out in rows, so arrows run straight: a target takes a row level with
 * the first trigger that reaches it, and a trigger reaching several new
 * targets grows to span their rows, one straight arrow into each. Only an
 * arrow into a target another trigger already placed has to turn: it leaves
 * its trigger at the free height nearest the target, turns once in a lane of
 * its own between the frames, and enters at the free height nearest where
 * it left. A label sits above its arrow's last stretch, and no other entry
 * comes within a label's height above it. */
export function layoutMapping(ir: FlowIR): FlowLayout {
  const [za, zb] = ir.zones;
  const w = boxWidth(ir.nodes, 160, 240);
  const left = ir.nodes.filter((n) => n.zone === za.id);
  const right = ir.nodes.filter((n) => n.zone === zb.id);
  const author = new Map(ir.nodes.map((n, i) => [n.id, i]));
  const labels = ir.edges.map((e) =>
    e.label !== "" ? labelBox(e.label) : null,
  );
  const edgesOf = (side: "from" | "to", id: string) =>
    ir.edges.map((e, i) => ({ e, i })).filter(({ e }) => e[side] === id);
  // An entry carrying a label needs the label's height and two gaps above it
  // free of other entries; a bare one only the attach-point spacing. Sized
  // for the tallest label, however many lines it wrapped to, and the rows
  // kept far enough apart that a label over a row's first entry stays clear
  // of the row above.
  const labelH = Math.max(labelLine, ...labels.map((l) => l?.h ?? 0));
  const room = labelH + 2 * labelGap + 8;
  const gap = Math.max(rowGap, labelH + labelGap);
  const spacing = (id: string) =>
    edgesOf("to", id).some(({ e }) => e.label !== "") ? room : 16;

  // Rows: each trigger in turn opens a row per target it is first to reach,
  // or one empty row when it reaches none first.
  type Row = { target?: string; y: number; h: number };
  const rows: Row[] = [];
  const rowOf = new Map<string, number>();
  const span = new Map<string, [number, number]>();
  for (const s of left) {
    const first = rows.length;
    for (const { e } of edgesOf("from", s.id)) {
      if (rowOf.has(e.to)) continue;
      rowOf.set(e.to, rows.length);
      rows.push({ target: e.to, y: 0, h: 0 });
    }
    if (rows.length === first) rows.push({ y: 0, h: 0 });
    span.set(s.id, [first, rows.length - 1]);
  }
  for (const t of right) {
    if (rowOf.has(t.id)) continue;
    rowOf.set(t.id, rows.length);
    rows.push({ target: t.id, y: 0, h: 0 });
  }

  // A target's arrows enter in even slots from its top, a spacing apart, so
  // a target reached four times is four slots tall and no taller. Its first
  // slot is the arrow from the trigger that placed it, which runs straight;
  // the others follow in the order their triggers stand.
  const slotted = (k: number, g: number) => (k < 2 ? 0 : up8(k * g + 16));
  // A trigger is centred on its straight arrow, so its other exits need room
  // on one side of the centre, the attach-point spacing apart and clear of
  // the corners.
  const exitRoom = (k: number) => (k < 2 ? 0 : up8(2 * (10 + 16 * (k - 1))));
  const boxes = new Map<string, NodeBox>();
  for (const n of ir.nodes) boxes.set(n.id, lay(n, "step", w));
  for (const t of right) {
    const b = boxes.get(t.id)!;
    b.h = Math.max(b.h, slotted(edgesOf("to", t.id).length, spacing(t.id)));
  }
  for (const s of left) {
    const b = boxes.get(s.id)!;
    b.h = Math.max(b.h, exitRoom(edgesOf("from", s.id).length));
  }
  rows.forEach((r) => (r.h = r.target ? boxes.get(r.target)!.h : 0));
  // A trigger alone in its row may need more height than its target.
  for (const s of left) {
    const [a, b] = span.get(s.id)!;
    if (a === b) rows[a].h = Math.max(rows[a].h, boxes.get(s.id)!.h);
  }
  let y = zonePad.top;
  for (const r of rows) {
    r.y = y;
    y += r.h + gap;
  }
  for (const t of right) {
    const r = rows[rowOf.get(t.id)!];
    boxes.get(t.id)!.y = r.y + (r.h - boxes.get(t.id)!.h) / 2;
  }

  // Which arrow placed each target, and the order its arrows enter in.
  const placer = new Map<string, number>();
  ir.edges.forEach((e, i) => {
    const [a, b] = span.get(e.from)!;
    const r = rowOf.get(e.to)!;
    if (r >= a && r <= b && !placer.has(e.to)) placer.set(e.to, i);
  });
  const slotOf = new Map<number, number>();
  for (const t of right) {
    const into = edgesOf("to", t.id)
      .map(({ i }) => i)
      .sort(
        (p, q) =>
          Number(placer.get(t.id) !== p) - Number(placer.get(t.id) !== q) ||
          author.get(ir.edges[p].from)! - author.get(ir.edges[q].from)! ||
          p - q,
      );
    into.forEach((i, j) => slotOf.set(i, j));
  }
  const slotY = (i: number) => {
    const t = boxes.get(ir.edges[i].to)!;
    const k = edgesOf("to", t.id).length;
    if (k < 2) return Math.round(t.y + t.h / 2);
    const g = spacing(t.id);
    return Math.round(t.y + (t.h - k * g) / 2 + g * (slotOf.get(i)! + 0.5));
  };

  const ys = new Map<number, number>();
  const yt = new Map<number, number>();
  const exits = new Map<string, number[]>();
  const take = <T>(m: Map<string, T[]>, id: string, v: T) =>
    m.set(id, [...(m.get(id) ?? []), v]);
  // Straight arrows first: each leaves its trigger level with its slot.
  const bent: number[] = [];
  ir.edges.forEach((e, i) => {
    if (placer.get(e.to) !== i) return bent.push(i);
    const c = slotY(i);
    ys.set(i, c);
    yt.set(i, c);
    take(exits, e.from, c);
  });
  // A trigger spanning several rows fills them; one alone in its row keeps
  // its own height, centred on its straight arrow, never a box stretched to
  // a tall target's height with nothing in it. A trigger with no straight
  // arrow is held by nothing on the right, so it closes up under the one
  // above instead of waiting for its row.
  let floor = zonePad.top;
  for (const s of left) {
    const [a, b] = span.get(s.id)!;
    const box = boxes.get(s.id)!;
    box.x = zonePad.side;
    if (a !== b) {
      box.y = rows[a].y;
      box.h = rows[b].y + rows[b].h - rows[a].y;
    } else {
      const level = exits.get(s.id)?.[0];
      box.y =
        level === undefined
          ? floor
          : Math.max(floor, Math.round(level - box.h / 2));
    }
    floor = box.y + box.h + rowGap;
  }
  // nearest is the free height on a box closest to want.
  const nearest = (
    box: NodeBox,
    want: number,
    free: (y: number) => boolean,
  ) => {
    let best: number | null = null;
    for (let y = Math.ceil(box.y + 10); y <= box.y + box.h - 10; y += 2) {
      if (
        free(y) &&
        (best === null || Math.abs(y - want) < Math.abs(best - want))
      )
        best = y;
    }
    return best ?? Math.round(box.y + box.h / 2);
  };
  bent.sort(
    (a, b) =>
      author.get(ir.edges[a].from)! - author.get(ir.edges[b].from)! || a - b,
  );
  for (const i of bent) {
    const e = ir.edges[i];
    const y1 = slotY(i);
    const y0 = nearest(boxes.get(e.from)!, y1, (y) =>
      (exits.get(e.from) ?? []).every((u) => Math.abs(u - y) >= 16),
    );
    ys.set(i, y0);
    yt.set(i, y1);
    take(exits, e.from, y0);
  }
  const srcBoxes = left.map((n) => boxes.get(n.id)!);
  const tgtBoxes = right.map((n) => boxes.get(n.id)!);

  // Lanes: an arrow that turns gets the first lane whose arrows it does not
  // meet vertically.
  const lanes: { lo: number; hi: number }[][] = [];
  const laneOf = new Map<number, number>();
  const turning = ir.edges
    .map((_, i) => i)
    .filter((i) => ys.get(i) !== yt.get(i))
    .sort(
      (a, b) =>
        Math.min(ys.get(a)!, yt.get(a)!) - Math.min(ys.get(b)!, yt.get(b)!) ||
        a - b,
    );
  for (const i of turning) {
    const lo = Math.min(ys.get(i)!, yt.get(i)!);
    const hi = Math.max(ys.get(i)!, yt.get(i)!);
    let k = lanes.findIndex((l) =>
      l.every((s) => hi + lane <= s.lo || lo >= s.hi + lane),
    );
    if (k < 0) {
      k = lanes.length;
      lanes.push([]);
    }
    lanes[k].push({ lo, hi });
    laneOf.set(i, k);
  }

  // A frame is as wide as its boxes, or its title where that is longer.
  const zoneAW = Math.max(w + 2 * zonePad.side, titleRoom(za.title));
  const zoneBW = Math.max(w + 2 * zonePad.side, titleRoom(zb.title));
  const firstLane = zoneAW + 24;
  const lastLane = firstLane + Math.max(0, lanes.length - 1) * lane;
  const labelW = Math.max(0, ...labels.map((l) => l?.w ?? 0));
  const xt = up8(lastLane + (labelW > 0 ? 16 + labelW + 16 : 40));
  const zbX = xt - zonePad.side;
  tgtBoxes.forEach((b) => (b.x = xt));

  const byId = new Map([...srcBoxes, ...tgtBoxes].map((b) => [b.id, b]));
  const edges: EdgeRoute[] = ir.edges.map((e, i) => {
    const xs = byId.get(e.from)!.x + w;
    const y0 = ys.get(i)!;
    const y1 = yt.get(i)!;
    const lx = firstLane + (laneOf.get(i) ?? 0) * lane;
    const points =
      y0 === y1
        ? [
            { x: xs, y: y0 },
            { x: xt, y: y1 },
          ]
        : [
            { x: xs, y: y0 },
            { x: lx, y: y0 },
            { x: lx, y: y1 },
            { x: xt, y: y1 },
          ];
    const route: EdgeRoute = {
      from: e.from,
      to: e.to,
      points,
      dashed: e.dashed,
      warn: e.dashed,
      head: e.head,
    };
    const l = labels[i];
    if (l) route.label = { ...l, x: xt - 16 - l.w, y: y1 - labelGap - l.h };
    return route;
  });

  const bottom = (bs: NodeBox[]) => Math.max(...bs.map((b) => b.y + b.h));
  const zoneH = Math.max(bottom(srcBoxes), bottom(tgtBoxes)) + zonePad.bottom;
  const zones: ZoneBox[] = [
    { id: za.id, title: za.title, x: 0, y: 0, w: zoneAW, h: zoneH },
    { id: zb.id, title: zb.title, x: zbX, y: 0, w: zoneBW, h: zoneH },
  ];
  const nodes = [...srcBoxes, ...tgtBoxes].sort(
    (a, b) => author.get(a.id)! - author.get(b.id)!,
  );
  return frame(nodes, edges, zones);
}

// ---- anything else ----

type Side = "NORTH" | "SOUTH" | "EAST" | "WEST";

/** layoutElk places a flowchart of any other shape with ELK's layered
 * layout, boxes sized and styled as the spine's are. A diamond gets a port
 * per edge on the side the convention gives it — in at the top, the first
 * branch straight on, the second right, the third left — and every end on a
 * diamond is moved from its bounding box onto its outline. */
export async function layoutElk(ir: FlowIR, elk: Elk): Promise<FlowLayout> {
  const tb = ir.direction === "TB";
  const w = boxWidth(
    ir.nodes.filter((n) => n.shape !== "decision"),
    160,
    240,
  );
  const boxes = new Map<string, NodeBox>();
  for (const n of ir.nodes)
    boxes.set(n.id, shaped(n, n.shape === "pill" ? "pill" : "step", w));

  const sides = {
    in: (tb ? "NORTH" : "WEST") as Side,
    out: (tb
      ? ["SOUTH", "EAST", "WEST"]
      : ["EAST", "SOUTH", "NORTH"]) as Side[],
  };
  const ports = new Map<string, ElkPort[]>();
  const addPort = (node: string, id: string, side: Side) => {
    const list = ports.get(node) ?? [];
    list.push({
      id,
      width: 0,
      height: 0,
      layoutOptions: { "elk.port.side": side },
    });
    ports.set(node, list);
  };
  const outCount = new Map<string, number>();
  const src: string[] = [];
  const dst: string[] = [];
  ir.edges.forEach((e, i) => {
    src.push(e.from);
    dst.push(e.to);
    if (e.from === e.to) return;
    if (boxes.get(e.from)!.kind === "decision") {
      const k = outCount.get(e.from) ?? 0;
      outCount.set(e.from, k + 1);
      src[i] = `${e.from}__out${i}`;
      addPort(e.from, src[i], sides.out[k] ?? sides.out[0]);
    }
    if (boxes.get(e.to)!.kind === "decision") {
      dst[i] = `${e.to}__in${i}`;
      addPort(e.to, dst[i], sides.in);
    }
  });

  const leaf = (n: FlowNodeIR): ElkNode => {
    const b = boxes.get(n.id)!;
    const p = ports.get(n.id);
    return {
      id: n.id,
      width: b.w,
      height: b.h,
      ...(p
        ? { ports: p, layoutOptions: { "elk.portConstraints": "FIXED_SIDE" } }
        : {}),
    };
  };
  const children: ElkNode[] = [];
  const zoneNodes = new Map<string, ElkNode>();
  for (const z of ir.zones) {
    const zn: ElkNode = {
      id: `zone:${z.id}`,
      children: [],
      layoutOptions: {
        "elk.padding": `[top=${zonePad.top},left=20,bottom=20,right=20]`,
      },
    };
    zoneNodes.set(z.id, zn);
    children.push(zn);
  }
  for (const n of ir.nodes) {
    const z = n.zone !== undefined ? zoneNodes.get(n.zone) : undefined;
    (z ? z.children! : children).push(leaf(n));
  }
  // A zone nothing landed in is not drawn: an empty frame says nothing.
  const kept = children.filter(
    (c) => !c.id.startsWith("zone:") || c.children!.length > 0,
  );
  const labels = ir.edges.map((e) =>
    e.label !== "" ? labelBox(e.label) : null,
  );
  const edges = ir.edges.map((e, i) =>
    elkEdge(`e${i}`, src[i], dst[i], e.label, labels[i]),
  );

  const root = await elk.layout({
    id: "root",
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": tb ? "DOWN" : "RIGHT",
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.hierarchyHandling": "INCLUDE_CHILDREN",
      "elk.json.edgeCoords": "ROOT",
      "elk.padding": "[top=0,left=0,bottom=0,right=0]",
      "elk.spacing.nodeNode": "32",
      "elk.layered.spacing.nodeNodeBetweenLayers": "40",
      "elk.spacing.edgeNode": "24",
      "elk.spacing.edgeEdge": "12",
      "elk.layered.spacing.edgeEdgeBetweenLayers": "12",
      "elk.layered.spacing.edgeNodeBetweenLayers": "24",
      "elk.spacing.edgeLabel": "10",
      "elk.edgeLabels.placement": "CENTER",
      "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
      // A cycle is broken at the edge that runs against the order written,
      // as the spine reads a loop: left to the engine, a state machine's
      // retry was kept and its forward path reversed, the start dot landing
      // in the middle of the picture. Greedy, so only a real cycle is
      // broken: plain MODEL_ORDER reversed every edge written against the
      // order, and a failure state mentioned early was hung at the top with
      // every later transition into it climbing up as a loop back. Shared
      // with flowcharts on purpose: a flowchart's retry is read the same
      // way, as the edge written against the order. Every corpus flowchart
      // ELK draws came out byte for byte the same with it.
      "elk.layered.cycleBreaking.strategy": "GREEDY_MODEL_ORDER",
      "elk.layered.crossingMinimization.forceNodeModelOrder": "true",
      "elk.layered.nodePlacement.strategy": "BRANDES_KOEPF",
      "elk.layered.nodePlacement.bk.fixedAlignment": "BALANCED",
    },
    children: kept,
    edges,
  });

  const zones: ZoneBox[] = [];
  const walk = (n: ElkNode, ox: number, oy: number) => {
    for (const c of n.children ?? []) {
      const x = ox + (c.x ?? 0);
      const y = oy + (c.y ?? 0);
      if (c.id.startsWith("zone:")) {
        const id = c.id.slice(5);
        const title = ir.zones.find((z) => z.id === id)?.title ?? id;
        zones.push({
          id,
          title,
          x: Math.round(x),
          y: Math.round(y),
          w: Math.round(c.width ?? 0),
          h: Math.round(c.height ?? 0),
        });
        walk(c, x, y);
        continue;
      }
      const b = boxes.get(c.id)!;
      b.x = Math.round(x);
      b.y = Math.round(y);
    }
  };
  walk(root, 0, 0);

  const routes: EdgeRoute[] = (root.edges ?? []).map((e, i) => {
    const ie = ir.edges[i];
    const points = elkPoints(e);
    const from = boxes.get(ie.from)!;
    const to = boxes.get(ie.to)!;
    if (points.length >= 2) {
      const last = points.length - 1;
      if (from.kind === "decision")
        points[0] = onDiamond(from, points[0], points[1]);
      if (to.kind === "decision")
        points[last] = onDiamond(to, points[last], points[last - 1]);
      if (dotted(from.kind)) points[0] = onCircle(from, points[0], points[1]);
      if (dotted(to.kind))
        points[last] = onCircle(to, points[last], points[last - 1]);
    }
    // An edge into a box wholly before its own, against the flow, is a loop
    // back, dashed as the spine draws it.
    const back = tb ? to.y + to.h <= from.y : to.x + to.w <= from.x;
    const route: EdgeRoute = {
      from: ie.from,
      to: ie.to,
      points,
      dashed: ie.dashed || back,
      warn: ie.dashed,
      head: ie.head,
    };
    const label = elkLabel(e, labels[i]);
    if (label) route.label = label;
    return route;
  });
  const nodes = ir.nodes.map((n) => boxes.get(n.id)!);
  widenForTitles(nodes, routes, zones);
  return frame(nodes, routes, zones);
}

/** elkEdge is an edge for the engine, carrying its label's measured box so
 * the layout makes room for it. Shared by every ELK layout. */
export function elkEdge(
  id: string,
  source: string,
  target: string,
  text: string,
  box: { w: number; h: number } | null,
): ElkExtendedEdge {
  return {
    id,
    sources: [source],
    targets: [target],
    ...(box
      ? { labels: [{ id: `${id}l`, text, width: box.w, height: box.h }] }
      : {}),
  };
}

/** elkPoints is an edge's route as the engine laid it out, on whole pixels
 * and without the points rounding made redundant. */
export function elkPoints(e: ElkExtendedEdge): Point[] {
  const sec = e.sections?.[0];
  if (!sec) return [];
  return tidy(
    [sec.startPoint, ...(sec.bendPoints ?? []), sec.endPoint].map((p) => ({
      x: Math.round(p.x),
      y: Math.round(p.y),
    })),
  );
}

/** elkLabel is an edge's label where the engine put it, or undefined for
 * an edge that had none. */
export function elkLabel(
  e: ElkExtendedEdge,
  box: { lines: string[]; w: number; h: number } | null,
): LabelBox | undefined {
  const l = e.labels?.[0];
  if (!l || !box) return undefined;
  return { ...box, x: Math.round(l.x ?? 0), y: Math.round(l.y ?? 0) };
}

/** widenForTitles makes every zone at least as wide as its title. ELK sizes
 * a zone from its members alone, and a minimum size set on it is not
 * honoured once children are laid out with it. So the room is made after
 * the layout: a band as wide as the shortfall is opened at the zone's right
 * edge, and everything right of that edge moves over by it. A horizontal
 * segment crossing the edge only grows and a vertical one moves whole, so
 * every arrow stays square. */
function widenForTitles(
  nodes: NodeBox[],
  routes: EdgeRoute[],
  zones: ZoneBox[],
): void {
  for (const z of [...zones].sort((a, b) => b.x + b.w - (a.x + a.w))) {
    const extra = titleRoom(z.title) - z.w;
    if (extra <= 0) continue;
    const cut = z.x + z.w;
    for (const b of nodes) if (b.x >= cut) b.x += extra;
    for (const r of routes) {
      for (const p of r.points) if (p.x >= cut) p.x += extra;
      if (r.label && r.label.x >= cut) r.label.x += extra;
    }
    for (const o of zones) {
      if (o === z) continue;
      if (o.x >= cut) o.x += extra;
      else if (o.x + o.w >= cut) o.w += extra;
    }
    z.w += extra;
  }
}

/** tidy drops the points rounding made redundant: a repeat, or the middle
 * of three in a line running one way. A route that doubles back keeps its
 * turn: dropping it would end the arrow somewhere else. */
export function tidy(points: Point[]): Point[] {
  const out: Point[] = [];
  const between = (a: number, b: number, c: number) =>
    (a <= b && b <= c) || (a >= b && b >= c);
  for (const p of points) {
    const last = out[out.length - 1];
    if (last && last.x === p.x && last.y === p.y) continue;
    const prev = out[out.length - 2];
    if (
      prev &&
      last &&
      ((prev.x === last.x && last.x === p.x && between(prev.y, last.y, p.y)) ||
        (prev.y === last.y && last.y === p.y && between(prev.x, last.x, p.x)))
    ) {
      out[out.length - 1] = p;
      continue;
    }
    out.push(p);
  }
  return out;
}

/** onCircle moves an edge's end from a dot's bounding box onto its
 * outline, along the segment that reaches it. An end beside the circle,
 * which the segment never meets, stays where it is. */
export function onCircle(n: NodeBox, end: Point, next: Point): Point {
  const cx = n.x + n.w / 2;
  const cy = n.y + n.h / 2;
  const r = n.w / 2;
  if (end.x === next.x) {
    const dx = end.x - cx;
    if (Math.abs(dx) >= r) return end;
    const dy = Math.sqrt(r * r - dx * dx);
    return { x: end.x, y: Math.round(cy + Math.sign(next.y - cy) * dy) };
  }
  const dy = end.y - cy;
  if (Math.abs(dy) >= r) return end;
  const dx = Math.sqrt(r * r - dy * dy);
  return { x: Math.round(cx + Math.sign(next.x - cx) * dx), y: end.y };
}

/** onDiamond moves an edge's end from the diamond's bounding box onto its
 * outline, along the segment that reaches it. */
export function onDiamond(n: NodeBox, end: Point, next: Point): Point {
  const cx = n.x + n.w / 2;
  const cy = n.y + n.h / 2;
  if (end.x === next.x) {
    const t = Math.max(0, 1 - Math.abs(end.x - cx) / (n.w / 2));
    return { x: end.x, y: cy + Math.sign(next.y - cy) * (n.h / 2) * t };
  }
  const t = Math.max(0, 1 - Math.abs(end.y - cy) / (n.h / 2));
  return { x: cx + Math.sign(next.x - cx) * (n.w / 2) * t, y: end.y };
}
