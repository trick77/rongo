import { useId, type ReactNode } from "react";
import type { MarkerHooks } from "./markdown";

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

/** The prompt's caps. A spec past them is rejected and shown as its text. */
export const caps = { nodes: 12, actors: 5, steps: 12 } as const;

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
 * this renderer draws. Null means the block is shown as the text it is. */
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
    if (nodes.length === 0 || nodes.length > caps.nodes) return null;
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
    if (actors.length === 0 || actors.length > caps.actors || steps.length > caps.steps) return null;
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
export type FlowLayout = { width: number; height: number; nodes: Placed[]; edges: EdgePath[]; ranks: string[][] };

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
    // into the target's right side.
    const laneX = colW + 24 + lanes++ * 12;
    const uy = u.y + u.h / 2;
    const vy = v.y + v.h / 2;
    edges.push({
      key,
      d: `M${u.x + u.w} ${uy} H${laneX} V${vy} H${v.x + v.w + 2}`,
      back: back.has(ei),
      label: e.label,
      lx: laneX,
      ly: (uy + vy) / 2,
    });
  });
  const width = colW + (lanes ? 24 + lanes * 12 : 0);
  return { width, height, nodes: placed, edges, ranks: ranks.map((r) => r.map((i) => spec.nodes[i].id)) };
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
      lx: self ? x1 + SELF_W + 6 : (x1 + x2) / 2,
      ly: self ? y + SELF_H / 2 + 4 : y - 6,
    });
    y += STEP_GAP + (self ? SELF_H : 0);
  });
  const width = actors.length * ACTOR_W + (actors.length - 1) * ACTOR_GAP;
  return { width, height: y - STEP_GAP + 20, actors, steps };
}

// ---- drawing ----

/** Chips draws one chip per marker, laid left to right and ending at `right`,
 * overhanging `top`. Semantics as markdown.tsx `text`: while the citations
 * are unknown every number wears the chip look; once known, a number with no
 * source behind it is plain text and has no hover. */
function Chips({ src, right, top, hooks, k }: { src: number[]; right: number; top: number; hooks: MarkerHooks; k: string }) {
  if (src.length === 0) return null;
  const known = hooks.backed !== undefined;
  const widths = src.map((m) => 8 + `[${m}]`.length * 6);
  const total = widths.reduce((a, b) => a + b, 0) + (src.length - 1) * 2;
  let x = right + 6 - total;
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
    return (
      <g
        key={`${k}-c${i}`}
        onMouseEnter={() => known && hooks.onHover?.(m)}
        onMouseLeave={() => known && hooks.onHover?.(null)}
      >
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
            <Chips src={nd.src} right={nd.x + nd.w - (nd.kind === "decision" ? 20 : 0)} top={nd.y} hooks={hooks} k={nd.id} />
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
          <Label lines={wrap(a.label, 16).slice(0, 1)} cx={a.x} cy={ACTOR_H / 2} />
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
          <Chips
            src={s.src}
            right={s.lx + (s.self ? s.label.length * CHAR_W : (s.label.length * CHAR_W) / 2) + 4 + s.src.length * 26}
            top={s.ly - 4}
            hooks={hooks}
            k={`s${s.i}`}
          />
        </g>
      ))}
    </>
  );
}

/** Diagram draws one parsed spec. The arrowhead markers carry a per-instance
 * id: a thread holds many answers, and url(#arrow) would resolve to the first
 * one's marker. */
export default function Diagram({ spec, hooks }: { spec: DiagramSpec; hooks: MarkerHooks }): ReactNode {
  const id = useId().replace(/[^A-Za-z0-9_-]/g, "");
  const head = `${id}-head`;
  const open = `${id}-open`;
  const layout = spec.type === "flow" ? layoutFlow(spec) : layoutSequence(spec);
  const title = spec.type === "flow" ? "Flow diagram" : "Sequence diagram";
  return (
    <div className="mt-3 overflow-x-auto rounded-ui-sm border border-border bg-panel p-3">
      <svg width={layout.width + 2 * PAD} height={layout.height + 2 * PAD} role="img" aria-label={title} className="block">
        <title>{title}</title>
        <defs>
          <marker id={head} markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
            <path d="M0 0L8 3L0 6z" className="fill-muted" />
          </marker>
          <marker id={open} markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
            <polyline points="0 0, 8 3, 0 6" className="fill-none stroke-muted" strokeWidth={1.2} />
          </marker>
        </defs>
        <g transform={`translate(${PAD},${PAD})`}>
          {spec.type === "flow" ? (
            <Flow layout={layout as FlowLayout} hooks={hooks} head={head} />
          ) : (
            <Sequence layout={layout as SeqLayout} hooks={hooks} head={head} open={open} />
          )}
        </g>
      </svg>
    </div>
  );
}
