import { useId, useRef, useState, type ReactNode, type RefObject } from "react";
import type { MarkerHooks } from "./markdown";
import { download, fileName, toSvgFile } from "./diagramExport";
import DiagramView from "./DiagramView";
import { DownloadIcon, ExpandIcon } from "./icons";

/**
 * One diagram in an answer: a fenced block tagged `diagram` holding a small
 * JSON spec, drawn here as React SVG. Never HTML, never a library: the spec
 * is model output, and innerHTML would make a prompt injection a script tag.
 *
 * Every node carries the markers it rests on in `src`, so the citation
 * invariant holds inside the picture. A chip on a node is the chip in the
 * prose (markdown.tsx `text`): the same look, the same hover hand-off to the
 * Sources pane, and a number no source backs stays plain text.
 *
 * The layout never measures text. jsdom has no getBBox, and the answer
 * re-renders on every streamed token; widths come from a character estimate,
 * so the coordinates are deterministic and a test can assert them.
 */

export type FlowKind = "start" | "end" | "step" | "decision";
export type FlowNode = { id: string; label: string; kind: FlowKind; src: number[] };
export type FlowEdge = { from: string; to: string; label?: string };
export type FlowSpec = { type: "flow"; nodes: FlowNode[]; edges: FlowEdge[] };
export type Actor = { id: string; label: string };
export type StepKind = "call" | "return" | "async";
export type SeqStep = { from: string; to: string; label: string; kind: StepKind; src: number[] };
export type SequenceSpec = { type: "sequence"; actors: Actor[]; steps: SeqStep[] };
export type DiagramSpec = FlowSpec | SequenceSpec;

const flowKinds: readonly string[] = ["start", "end", "step", "decision"];
const stepKinds: readonly string[] = ["call", "return", "async"];

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isSrc(v: unknown): v is number[] {
  return Array.isArray(v) && v.every((n) => Number.isInteger(n) && (n as number) > 0);
}

function ids(items: Record<string, unknown>[]): Set<string> | null {
  const seen = new Set<string>();
  for (const it of items) {
    if (typeof it.id !== "string" || it.id === "" || seen.has(it.id)) return null;
    seen.add(it.id);
  }
  return seen;
}

/** parseDiagram reads the fence body, or returns null when it is not a spec
 * this renderer draws. Null means the block is shown as the text it is.
 *
 * Size is not a reason to return null. The prompt asks for at most 12 nodes,
 * 5 actors and 12 steps because that is what reads well, and this file used
 * to enforce those same numbers: a model that answered with eight actors had
 * its whole diagram thrown away and the reader got the JSON. A picture one
 * actor too wide is still the picture; the box it sits in scrolls. So the
 * gates here are structural only - the shape has to be drawable, and every
 * id a step or an edge names has to be one the spec declared. */
export function parseDiagram(body: string): DiagramSpec | null {
  let raw: unknown;
  try {
    raw = JSON.parse(body);
  } catch {
    return null;
  }
  if (!isRecord(raw)) return null;
  if (raw.type === "flow") {
    const { nodes, edges } = raw;
    if (!Array.isArray(nodes) || !Array.isArray(edges)) return null;
    if (nodes.length === 0) return null;
    if (!nodes.every(isRecord) || !edges.every(isRecord)) return null;
    const known = ids(nodes);
    if (!known) return null;
    const out: FlowNode[] = [];
    for (const n of nodes) {
      const kind = n.kind ?? "step";
      if (typeof n.label !== "string" || !flowKinds.includes(kind as string)) return null;
      if (n.src !== undefined && !isSrc(n.src)) return null;
      out.push({ id: n.id as string, label: n.label, kind: kind as FlowKind, src: (n.src as number[]) ?? [] });
    }
    const es: FlowEdge[] = [];
    for (const e of edges) {
      if (typeof e.from !== "string" || typeof e.to !== "string") return null;
      if (!known.has(e.from) || !known.has(e.to)) return null;
      if (e.label !== undefined && typeof e.label !== "string") return null;
      es.push({ from: e.from, to: e.to, label: e.label as string | undefined });
    }
    return { type: "flow", nodes: out, edges: es };
  }
  if (raw.type === "sequence") {
    const { actors, steps } = raw;
    if (!Array.isArray(actors) || !Array.isArray(steps)) return null;
    if (actors.length === 0) return null;
    if (!actors.every(isRecord) || !steps.every(isRecord)) return null;
    const known = ids(actors);
    if (!known) return null;
    const as: Actor[] = [];
    for (const a of actors) {
      if (typeof a.label !== "string") return null;
      as.push({ id: a.id as string, label: a.label });
    }
    const ss: SeqStep[] = [];
    for (const s of steps) {
      const kind = s.kind ?? "call";
      if (typeof s.from !== "string" || typeof s.to !== "string" || typeof s.label !== "string") return null;
      if (!known.has(s.from) || !known.has(s.to) || !stepKinds.includes(kind as string)) return null;
      if (s.src !== undefined && !isSrc(s.src)) return null;
      ss.push({ from: s.from, to: s.to, label: s.label, kind: kind as StepKind, src: (s.src as number[]) ?? [] });
    }
    return { type: "sequence", actors: as, steps: ss };
  }
  return null;
}

// ---- geometry ----

export const NODE_W = 150;
const COL_GAP = 32;
const RANK_GAP = 48;
const LINE_H = 15;
const CHAR_W = 6.5;
const WRAP = 20;
const MAX_LINES = 3;
const CHIP_H = 14;
/** The chips overhang a node's top-right corner; the origin offset and the
 * total size reserve this much so no chip is clipped. */
export const PAD = 8;

/** wrap breaks a label into at most three lines of about `cols` characters
 * on word boundaries, an ellipsis on the last line when it does not fit. */
export function wrap(label: string, cols = WRAP): string[] {
  const lines: string[] = [];
  let cur = "";
  for (const word of label.split(/\s+/).filter(Boolean)) {
    if (cur === "") cur = word;
    else if (cur.length + 1 + word.length <= cols) cur += " " + word;
    else {
      lines.push(cur);
      cur = word;
    }
  }
  if (cur !== "") lines.push(cur);
  if (lines.length > MAX_LINES) {
    const kept = lines.slice(0, MAX_LINES);
    kept[MAX_LINES - 1] = kept[MAX_LINES - 1].slice(0, cols - 1) + "…";
    return kept;
  }
  return lines.map((l) => (l.length > cols ? l.slice(0, cols - 1) + "…" : l));
}

/** chipsWidth is what a node's markers occupy. The layout needs it before
 * anything is drawn: the SVG has no viewBox, so whatever falls outside the
 * computed width is clipped away, and clipped chips would take a node's
 * sources with them. */
export function chipsWidth(src: number[]): number {
  if (src.length === 0) return 0;
  return src.reduce((w, m) => w + 8 + `[${m}]`.length * 6, 0) + (src.length - 1) * 2;
}

/** labelWidth estimates a run of text, the one measurement this file makes. */
export function labelWidth(label: string | undefined): number {
  return label ? label.length * CHAR_W : 0;
}

/** truncate keeps one line, marking a cut so a shortened name cannot be read
 * as a real one: an actor "Grant Store Service" is not "Grant Store". */
export function truncate(label: string, cols: number): string {
  return label.length > cols ? label.slice(0, cols - 1) + "…" : label;
}

export type Placed = {
  id: string;
  kind: FlowKind;
  src: number[];
  x: number;
  y: number;
  w: number;
  h: number;
  lines: string[];
  rank: number;
};
export type EdgePath = { key: string; d: string; back: boolean; label?: string; lx: number; ly: number };
export type FlowLayout = {
  width: number;
  height: number;
  /** How far left of the origin anything reaches, as a positive shift the
   * drawing applies: a decision in the leftmost column exits by its side, and
   * its label is centred on that exit. */
  originX: number;
  nodes: Placed[];
  edges: EdgePath[];
  ranks: string[][];
};

/** layoutFlow places the nodes top-down: back edges found by DFS are set
 * aside, ranks are the longest path from a source, a rank orders its nodes by
 * the mean position of their parents, and back and skip edges run in a lane
 * to the right so nothing crosses a node body. */
export function layoutFlow(spec: FlowSpec): FlowLayout {
  const n = spec.nodes.length;
  const index = new Map(spec.nodes.map((nd, i) => [nd.id, i]));
  const out: number[][] = spec.nodes.map(() => []);
  const indeg = new Array<number>(n).fill(0);
  spec.edges.forEach((e, ei) => {
    out[index.get(e.from)!].push(ei);
    indeg[index.get(e.to)!]++;
  });

  // Back edges: an edge into a node still on the DFS stack.
  const back = new Set<number>();
  const state = new Array<number>(n).fill(0);
  const visit = (u: number) => {
    state[u] = 1;
    for (const ei of out[u]) {
      const v = index.get(spec.edges[ei].to)!;
      if (state[v] === 1) back.add(ei);
      else if (state[v] === 0) visit(v);
    }
    state[u] = 2;
  };
  const roots = spec.nodes.map((_, i) => i).filter((i) => indeg[i] === 0);
  for (const r of roots) visit(r);
  for (let i = 0; i < n; i++) if (state[i] === 0) visit(i);

  // Ranks: longest path over the remaining DAG.
  const rank = new Array<number>(n).fill(0);
  const deg = new Array<number>(n).fill(0);
  spec.edges.forEach((e, ei) => {
    if (!back.has(ei)) deg[index.get(e.to)!]++;
  });
  const queue = spec.nodes.map((_, i) => i).filter((i) => deg[i] === 0);
  for (let q = 0; q < queue.length; q++) {
    const u = queue[q];
    for (const ei of out[u]) {
      if (back.has(ei)) continue;
      const v = index.get(spec.edges[ei].to)!;
      rank[v] = Math.max(rank[v], rank[u] + 1);
      if (--deg[v] === 0) queue.push(v);
    }
  }

  // Order within a rank by the parents' mean position, spec order on ties.
  const depth = Math.max(...rank) + 1;
  const ranks: number[][] = Array.from({ length: depth }, () => []);
  spec.nodes.forEach((_, i) => ranks[rank[i]].push(i));
  const pos = new Array<number>(n).fill(0);
  ranks[0].forEach((i, j) => (pos[i] = j));
  for (let r = 1; r < depth; r++) {
    const bary = (i: number) => {
      const parents = spec.edges
        .filter((e, ei) => !back.has(ei) && index.get(e.to) === i && rank[index.get(e.from)!] < r)
        .map((e) => pos[index.get(e.from)!]);
      return parents.length ? parents.reduce((a, b) => a + b, 0) / parents.length : Number.MAX_SAFE_INTEGER;
    };
    ranks[r].sort((a, b) => bary(a) - bary(b) || a - b);
    ranks[r].forEach((i, j) => (pos[i] = j));
  }

  // Sizes and coordinates.
  const placed: Placed[] = spec.nodes.map((nd, i) => {
    const lines = wrap(nd.label);
    let h = Math.max(40, 16 + lines.length * LINE_H);
    if (nd.kind === "decision") h += 16;
    return { id: nd.id, kind: nd.kind, src: nd.src, x: 0, y: 0, w: NODE_W, h, lines, rank: rank[i] };
  });
  const colW = Math.max(...ranks.map((r) => r.length * NODE_W + (r.length - 1) * COL_GAP));
  let y = 0;
  for (const r of ranks) {
    const rankH = Math.max(...r.map((i) => placed[i].h));
    const rankW = r.length * NODE_W + (r.length - 1) * COL_GAP;
    r.forEach((i, j) => {
      placed[i].x = (colW - rankW) / 2 + j * (NODE_W + COL_GAP);
      placed[i].y = y + (rankH - placed[i].h) / 2;
    });
    y += rankH + RANK_GAP;
  }
  const height = y - RANK_GAP;

  // Edges.
  const edges: EdgePath[] = [];
  let lanes = 0;
  spec.edges.forEach((e, ei) => {
    const u = placed[index.get(e.from)!];
    const v = placed[index.get(e.to)!];
    const cu = u.x + u.w / 2;
    const cv = v.x + v.w / 2;
    const key = `${e.from}->${e.to}-${ei}`;
    if (!back.has(ei) && v.rank === u.rank + 1) {
      const bottom = u.y + u.h;
      const mid = bottom + RANK_GAP / 2;
      if (cu === cv) {
        edges.push({ key, d: `M${cu} ${bottom} V${v.y - 2}`, back: false, label: e.label, lx: cu, ly: mid });
        return;
      }
      // A decision leaves by its side point toward the branch, a step by its
      // bottom; either way the elbow sits halfway down the gap.
      const side = u.kind === "decision";
      const sx = side ? (cv < cu ? u.x : u.x + u.w) : cu;
      const sy = side ? u.y + u.h / 2 : bottom;
      const d = side ? `M${sx} ${sy} H${cv} V${v.y - 2}` : `M${sx} ${sy} V${mid} H${cv} V${v.y - 2}`;
      edges.push({ key, d, back: false, label: e.label, lx: (sx + cv) / 2, ly: side ? sy + 20 : mid });
      return;
    }
    // Skip and back edges: out of the right side, down (or up) the lane,
    // into the target's right side. A label sits to the RIGHT of its lane,
    // never centred on it: centred, a long one reaches back over the node
    // column, which the edges are painted under.
    const laneX = colW + 24 + lanes++ * 12;
    const uy = u.y + u.h / 2;
    const vy = v.y + v.h / 2;
    edges.push({
      key,
      d: `M${u.x + u.w} ${uy} H${laneX} V${vy} H${v.x + v.w + 2}`,
      back: back.has(ei),
      label: e.label,
      lx: laneX + (e.label ? labelWidth(e.label) / 2 + 8 : 0),
      ly: (uy + vy) / 2,
    });
  });
  // Whatever sits past this is clipped, so the width is the rightmost thing
  // drawn: the columns, the lanes, an edge label, a node's chips.
  const width = Math.max(
    colW + (lanes ? 24 + lanes * 12 : 0),
    ...edges.map((e) => (e.label ? e.lx + (labelWidth(e.label) + 8) / 2 : 0)),
    ...placed.map((nd) => nd.x + nd.w + 6),
  );
  // And the leftmost: an edge label centred on a leftmost decision's side
  // exit starts left of the origin, where there is only the padding to
  // spare. The drawing shifts right by this, rather than losing the text.
  const originX = Math.max(0, ...edges.map((e) => (e.label ? (labelWidth(e.label) + 8) / 2 - e.lx : 0)));
  return {
    width: width + originX,
    height,
    originX,
    nodes: placed,
    edges,
    ranks: ranks.map((r) => r.map((i) => spec.nodes[i].id)),
  };
}

const ACTOR_W = 110;
const ACTOR_GAP = 30;
const ACTOR_H = 36;
const STEP_GAP = 40;
const SELF_W = 30;
const SELF_H = 20;

export type PlacedStep = {
  i: number;
  y: number;
  x1: number;
  x2: number;
  self: boolean;
  kind: StepKind;
  label: string;
  src: number[];
  d: string;
  lx: number;
  ly: number;
  /** Where this step's chips start, and where its drawing ends. */
  chipsX: number;
  right: number;
};
export type SeqLayout = { width: number; height: number; actors: (Actor & { x: number })[]; steps: PlacedStep[] };

/** layoutSequence puts the actors in a row and the steps top-down, one row
 * each; a self message takes a little more. */
export function layoutSequence(spec: SequenceSpec): SeqLayout {
  const actors = spec.actors.map((a, i) => ({ ...a, x: i * (ACTOR_W + ACTOR_GAP) + ACTOR_W / 2 }));
  const xOf = new Map(actors.map((a) => [a.id, a.x]));
  const steps: PlacedStep[] = [];
  let y = ACTOR_H + 34;
  spec.steps.forEach((s, i) => {
    const x1 = xOf.get(s.from)!;
    const x2 = xOf.get(s.to)!;
    const self = x1 === x2;
    const d = self
      ? `M${x1} ${y} H${x1 + SELF_W} V${y + SELF_H} H${x1 + 2}`
      : `M${x1} ${y} H${x2 + (x2 > x1 ? -2 : 2)}`;
    // A self message anchors its label at the loop; every other step centres
    // it over the arrow. Either way the chips follow the label, and the two
    // together decide how far right this step reaches.
    const lx = self ? x1 + SELF_W + 6 : (x1 + x2) / 2;
    const labelRight = lx + labelWidth(s.label) / (self ? 1 : 2);
    const chipsX = labelRight + 4;
    steps.push({
      i,
      y,
      x1,
      x2,
      self,
      kind: s.kind,
      label: s.label,
      src: s.src,
      d,
      lx,
      ly: self ? y + SELF_H / 2 + 4 : y - 6,
      chipsX,
      right: chipsX + chipsWidth(s.src),
    });
    y += STEP_GAP + (self ? SELF_H : 0);
  });
  // A label or a chip past the last lifeline is still drawn, and the SVG has
  // no viewBox: a self call on the rightmost actor would otherwise lose both.
  const width = Math.max(
    actors.length * ACTOR_W + (actors.length - 1) * ACTOR_GAP,
    ...steps.map((s) => s.right),
  );
  return { width, height: y - STEP_GAP + 20, actors, steps };
}

// ---- drawing ----

/** Chips draws one chip per marker, laid left to right and ending at `right`,
 * overhanging `top`. Semantics as markdown.tsx `text`: while the citations
 * are unknown every number wears the chip look; once known, a number with no
 * source behind it is plain text and has no hover. */
function Chips({ src, left, top, hooks, k }: { src: number[]; left: number; top: number; hooks: MarkerHooks; k: string }) {
  if (src.length === 0) return null;
  const known = hooks.backed !== undefined;
  const widths = src.map((m) => 8 + `[${m}]`.length * 6);
  let x = left;
  const y = top - CHIP_H / 2;
  return src.map((m, i) => {
    const w = widths[i];
    const cx = x + w / 2;
    x += w + 2;
    const plain = known && !hooks.backed!.has(m);
    if (plain) {
      return (
        <text key={`${k}-c${i}`} x={cx} y={y + 11} textAnchor="middle" className="fill-muted font-mono text-[10px]">
          [{m}]
        </text>
      );
    }
    // Once its source is known the chip opens it, as a chip in the prose
    // does: on a tablet there is no hover and no pane, and a chip that
    // cannot be tapped leaves the reader no way to the source.
    const open = known && hooks.onOpen;
    return (
      <g
        key={`${k}-c${i}`}
        onMouseEnter={() => known && hooks.onHover?.(m)}
        onMouseLeave={() => known && hooks.onHover?.(null)}
        onClick={open ? () => hooks.onOpen?.(m) : undefined}
        role={open ? "button" : undefined}
        tabIndex={open ? 0 : undefined}
        // Space scrolls the page unless it is taken here; a real <button>
        // in the prose gets that for free, an SVG group does not.
        onKeyDown={
          open
            ? (e) => {
                if (e.key !== "Enter" && e.key !== " ") return;
                e.preventDefault();
                hooks.onOpen?.(m);
              }
            : undefined
        }
        className={open ? "cursor-pointer" : undefined}
      >
        {/* A 26x14 chip is a mouse's target, and an SVG group cannot take
            padding, so the hit area has to be drawn. The height is where the
            room is: 14 -> 32. Sideways it may grow by at most half the 2px
            the chips are laid apart above — any more and this rect would
            reach over the neighbouring chip, which comes later in document
            order and would win the tap, so the right edge of [1] would open
            source 2. */}
        {open && (
          <rect
            x={cx - w / 2 - 1}
            y={y - 9}
            width={w + 2}
            height={CHIP_H + 18}
            className="fill-transparent"
            // A downloaded file has nothing to tap: diagramExport.ts drops
            // these by the marker rather than by guessing at a fill.
            data-export="skip"
          />
        )}
        <rect x={cx - w / 2} y={y} width={w} height={CHIP_H} rx={3} className="fill-accent-dim" />
        <text x={cx} y={y + 11} textAnchor="middle" className="fill-accent-strong font-mono text-[10px] font-semibold">
          [{m}]
        </text>
      </g>
    );
  });
}

function Label({ lines, cx, cy }: { lines: string[]; cx: number; cy: number }) {
  const top = cy - ((lines.length - 1) * LINE_H) / 2 + 4;
  return (
    <text textAnchor="middle" className="fill-ink text-[13px]">
      {lines.map((l, i) => (
        <tspan key={i} x={cx} y={top + i * LINE_H}>
          {l}
        </tspan>
      ))}
    </text>
  );
}

function EdgeLabel({ label, x, y }: { label?: string; x: number; y: number }) {
  if (!label) return null;
  const w = label.length * CHAR_W + 8;
  return (
    <g>
      <rect x={x - w / 2} y={y - 8} width={w} height={16} rx={3} className="fill-panel" />
      <text x={x} y={y + 4} textAnchor="middle" className="fill-muted font-mono text-[11px]">
        {label}
      </text>
    </g>
  );
}

function Flow({ layout, hooks, head }: { layout: FlowLayout; hooks: MarkerHooks; head: string }) {
  return (
    <>
      {layout.edges.map((e) => (
        <g key={e.key}>
          <path d={e.d} markerEnd={`url(#${head})`} className="fill-none stroke-muted" strokeWidth={1.2} />
          <EdgeLabel label={e.label} x={e.lx} y={e.ly} />
        </g>
      ))}
      {layout.nodes.map((nd) => {
        const cx = nd.x + nd.w / 2;
        const cy = nd.y + nd.h / 2;
        return (
          <g key={nd.id}>
            {nd.kind === "decision" ? (
              <polygon
                points={`${cx},${nd.y} ${nd.x + nd.w},${cy} ${cx},${nd.y + nd.h} ${nd.x},${cy}`}
                className="fill-active stroke-border"
              />
            ) : (
              <rect
                x={nd.x}
                y={nd.y}
                width={nd.w}
                height={nd.h}
                rx={nd.kind === "step" ? 8 : nd.h / 2}
                className="fill-active stroke-border"
              />
            )}
            <Label lines={nd.lines} cx={cx} cy={cy} />
            <Chips
              src={nd.src}
              left={nd.x + nd.w + 6 - (nd.kind === "decision" ? 20 : 0) - chipsWidth(nd.src)}
              top={nd.y}
              hooks={hooks}
              k={nd.id}
            />
          </g>
        );
      })}
    </>
  );
}

function Sequence({ layout, hooks, head, open }: { layout: SeqLayout; hooks: MarkerHooks; head: string; open: string }) {
  const bottom = layout.height;
  return (
    <>
      {layout.actors.map((a) => (
        <g key={a.id}>
          <line x1={a.x} y1={ACTOR_H} x2={a.x} y2={bottom} className="stroke-border" strokeDasharray="4 4" />
          <rect x={a.x - ACTOR_W / 2} y={0} width={ACTOR_W} height={ACTOR_H} rx={8} className="fill-active stroke-border" />
          <Label lines={[truncate(a.label, 16)]} cx={a.x} cy={ACTOR_H / 2} />
        </g>
      ))}
      {layout.steps.map((s) => (
        <g key={s.i}>
          <path
            d={s.d}
            markerEnd={`url(#${s.kind === "async" ? open : head})`}
            className="fill-none stroke-muted"
            strokeWidth={1.2}
            strokeDasharray={s.kind === "return" ? "4 3" : undefined}
          />
          <text
            x={s.lx}
            y={s.ly}
            textAnchor={s.self ? "start" : "middle"}
            className="fill-ink-dim font-mono text-[11px]"
          >
            {s.label}
          </text>
          <Chips src={s.src} left={s.chipsX} top={s.ly - 4} hooks={hooks} k={`s${s.i}`} />
        </g>
      ))}
    </>
  );
}

/** diagramTitle names the picture, in the accessible label, the file and the
 * full view's header. */
export function diagramTitle(spec: DiagramSpec): string {
  return spec.type === "flow" ? "Flow diagram" : "Sequence diagram";
}

/** diagramSize is the drawing's intrinsic size. The full view scales itself
 * against it, and does so without measuring anything: the layout is a
 * character estimate, so this is the same number on the server, in a test and
 * on a phone. */
export function diagramSize(spec: DiagramSpec): { width: number; height: number } {
  const layout = spec.type === "flow" ? layoutFlow(spec) : layoutSequence(spec);
  return { width: layout.width + 2 * PAD, height: layout.height + 2 * PAD };
}

/** DiagramSvg draws one parsed spec. The arrowhead markers carry a
 * per-instance id: a thread holds many answers, and url(#arrow) would resolve
 * to the first one's marker.
 *
 * Separate from the card below it because the full view (DiagramView.tsx)
 * draws the same picture in a different box, and a second renderer would be a
 * second drawing to keep in step. */
export function DiagramSvg({
  spec,
  hooks,
  svgRef,
}: {
  spec: DiagramSpec;
  hooks: MarkerHooks;
  svgRef?: RefObject<SVGSVGElement | null>;
}): ReactNode {
  const id = useId().replace(/[^A-Za-z0-9_-]/g, "");
  const head = `${id}-head`;
  const open = `${id}-open`;
  const layout = spec.type === "flow" ? layoutFlow(spec) : layoutSequence(spec);
  const title = diagramTitle(spec);
  return (
    // font-sans explicitly: the diagram sits inside the answer's .ui-markdown
    // wrapper, which is serif prose. A node label is a name from the code,
    // not prose, and it reads as the rest of the chrome does.
    //
    // Sized in px and deliberately given no viewBox, so on a phone it scrolls
    // inside its box rather than scaling. A viewBox would fit a 530px sequence
    // into 310px at 0.55: the 10px chip text lands near 5.5px and the chip
    // itself near 14x8, which is neither readable nor tappable — and a diagram
    // that cites like prose has to be both. With the width attribute and no
    // viewBox, any CSS that narrows the element clips the drawing instead of
    // scaling it, so it must keep its own intrinsic width. The full view
    // scales it on purpose, with a transform, and only downwards.
    <svg
      ref={svgRef}
      width={layout.width + 2 * PAD}
      height={layout.height + 2 * PAD}
      role="img"
      aria-label={title}
      className="block font-sans"
    >
      <title>{title}</title>
      <defs>
        <marker id={head} markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
          <path d="M0 0L8 3L0 6z" className="fill-muted" />
        </marker>
        <marker id={open} markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
          <polyline points="0 0, 8 3, 0 6" className="fill-none stroke-muted" strokeWidth={1.2} />
        </marker>
      </defs>
      <g transform={`translate(${PAD + ("originX" in layout ? layout.originX : 0)},${PAD})`}>
        {spec.type === "flow" ? (
          <Flow layout={layout as FlowLayout} hooks={hooks} head={head} />
        ) : (
          <Sequence layout={layout as SeqLayout} hooks={hooks} head={head} open={open} />
        )}
      </g>
    </svg>
  );
}

/** Diagram is the picture as it sits in an answer: the scrolling card, and the
 * two ways out of it — the full view, and the file.
 *
 * The scroller is an inner element rather than the card itself. The controls
 * are positioned against the card, and a positioned child of a scroller
 * travels with its content: on a wide diagram the buttons would slide off the
 * left edge on the first drag. */
export default function Diagram({ spec, hooks }: { spec: DiagramSpec; hooks: MarkerHooks }): ReactNode {
  const svg = useRef<SVGSVGElement>(null);
  const [full, setFull] = useState(false);
  const title = diagramTitle(spec);

  function save() {
    if (svg.current) download(fileName(spec), toSvgFile(svg.current));
  }

  return (
    <div className="relative mt-3 rounded-ui-sm border border-border bg-panel p-3">
      {/* Always drawn, never revealed on hover: a phone has no hover, and the
          full view is the only way it can see a wide diagram whole. The fade
          is Threads.tsx's, for the same reason — the drawing scrolls under
          the buttons and would otherwise end against them mid-line. */}
      {/* pointer-events-none on the strip, restored on the buttons: the box
          is ~110px wide with its fade, and it lies over the top-right of the
          drawing. Solid, it would swallow the taps meant for a chip that
          scrolled under it — the same target the hit rect in Chips is drawn
          to protect. */}
      <div className="pointer-events-none absolute top-1.5 right-1.5 z-10 flex items-center gap-0.5 bg-gradient-to-l from-panel from-70% to-transparent pl-6">
        <button
          type="button"
          onClick={() => setFull(true)}
          title="Full view"
          aria-label={`${title}: full view`}
          className="pointer-events-auto grid h-7 w-7 place-items-center rounded-ui-sm text-faint hover:bg-active hover:text-ink-dim"
        >
          <ExpandIcon />
        </button>
        <button
          type="button"
          onClick={save}
          title="Download SVG"
          aria-label={`${title}: download SVG`}
          className="pointer-events-auto grid h-7 w-7 place-items-center rounded-ui-sm text-faint hover:bg-active hover:text-ink-dim"
        >
          <DownloadIcon />
        </button>
      </div>
      <div className="max-w-full overflow-x-auto overscroll-x-contain">
        <DiagramSvg spec={spec} hooks={hooks} svgRef={svg} />
      </div>
      {full && <DiagramView spec={spec} hooks={hooks} onClose={() => setFull(false)} />}
    </div>
  );
}
