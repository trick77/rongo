import type { FlowLayout, NodeBox, Point } from "./diagramLayout";
import { textWidth } from "./diagramTheme";

/**
 * A layout's taste gate as code: the connector rules diagram-design holds
 * every hand-drawn diagram to (references/primitives-core.md), checked on a
 * computed layout. Every arrow straight or bent at right angles, leaving and
 * entering a box square to its edge; never behind a box it does not
 * connect; no two arrows on one stretch of line or one attach point; every
 * label clear of its line and of every box; every label inside its box.
 * corpus.test.ts runs it over every corpus diagram, so a layout change that
 * breaks one fails a test rather than a picture.
 *
 * Returns what is wrong, in words, empty when nothing is.
 */

type Rect = { x: number; y: number; w: number; h: number };

/** The least clear space between a label and its own line. */
export const minLabelGap = 6;

export function check(l: FlowLayout): string[] {
  const out: string[] = [];
  const byId = new Map(l.nodes.map((n) => [n.id, n]));

  for (const n of l.nodes) {
    if (n.kind === "decision") continue;
    for (const line of n.lines) {
      if (textWidth(line, n.size, n.face) > n.w - 32)
        out.push(`${n.id}: "${line}" overflows its box`);
    }
  }

  l.edges.forEach((e, i) => {
    const name = `${e.from}->${e.to}`;
    if (e.points.length < 2) {
      out.push(`${name}: no route`);
      return;
    }
    for (const [a, b] of segments(e.points)) {
      if (a.x !== b.x && a.y !== b.y) out.push(`${name}: diagonal segment`);
    }
    const from = byId.get(e.from)!;
    const to = byId.get(e.to)!;
    if (
      from.kind !== "decision" &&
      !perpendicular(from, e.points[0], e.points[1])
    )
      out.push(`${name}: leaves ${e.from} along its border`);
    if (
      to.kind !== "decision" &&
      !perpendicular(
        to,
        e.points[e.points.length - 1],
        e.points[e.points.length - 2],
      )
    )
      out.push(`${name}: enters ${e.to} along its border`);

    // Rule 5: never behind a box that is not an end.
    for (const n of l.nodes) {
      if (n.id === e.from || n.id === e.to) continue;
      if (segments(e.points).some(([a, b]) => crosses(a, b, inset(n, 1))))
        out.push(`${name}: passes behind ${n.id}`);
    }

    if (e.label) {
      const m = e.label;
      // Rule 2: the label clear of its own line.
      const d = Math.min(
        ...segments(e.points).map(([a, b]) => distance(m, a, b)),
      );
      if (d < minLabelGap) out.push(`${name}: label ${d}px from its line`);
      // Rule 6: the label clear of every box.
      for (const n of l.nodes) {
        if (overlaps(m, n)) out.push(`${name}: label over ${n.id}`);
      }
    }

    // Rules 3 and 4: no two connectors share a stretch or an end point.
    for (const f of l.edges.slice(i + 1)) {
      const other = `${f.from}->${f.to}`;
      if (
        segments(e.points).some(([a, b]) =>
          segments(f.points).some(([c, d]) => collinear(a, b, c, d)),
        )
      )
        out.push(`${name} and ${other}: run on top of each other`);
      const ends = [e.points[0], e.points[e.points.length - 1]];
      const fends = [f.points[0], f.points[f.points.length - 1]];
      if (ends.some((p) => fends.some((q) => p.x === q.x && p.y === q.y)))
        out.push(`${name} and ${other}: share an attach point`);
      if (e.label && f.label && overlaps(e.label, f.label))
        out.push(`${name} and ${other}: labels overlap`);
    }
  });
  return out;
}

function segments(points: Point[]): [Point, Point][] {
  const out: [Point, Point][] = [];
  for (let i = 1; i < points.length; i++) out.push([points[i - 1], points[i]]);
  return out;
}

/** perpendicular says the first segment leaves the box straight out of the
 * edge its end lies on, not along it. */
function perpendicular(n: NodeBox, end: Point, next: Point): boolean {
  const onSide = end.x === n.x || end.x === n.x + n.w;
  const onTopBottom = end.y === n.y || end.y === n.y + n.h;
  if (onSide && !onTopBottom) return end.y === next.y;
  if (onTopBottom && !onSide) return end.x === next.x;
  return false;
}

function inset(r: Rect, by: number): Rect {
  return { x: r.x + by, y: r.y + by, w: r.w - 2 * by, h: r.h - 2 * by };
}

/** crosses says an axis-aligned segment enters the inside of a rect. */
function crosses(a: Point, b: Point, r: Rect): boolean {
  const x0 = Math.min(a.x, b.x);
  const x1 = Math.max(a.x, b.x);
  const y0 = Math.min(a.y, b.y);
  const y1 = Math.max(a.y, b.y);
  return x1 > r.x && x0 < r.x + r.w && y1 > r.y && y0 < r.y + r.h;
}

function overlaps(a: Rect, b: Rect): boolean {
  return (
    a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
  );
}

/** distance from a rect to an axis-aligned segment. */
function distance(r: Rect, a: Point, b: Point): number {
  const x0 = Math.min(a.x, b.x);
  const x1 = Math.max(a.x, b.x);
  const y0 = Math.min(a.y, b.y);
  const y1 = Math.max(a.y, b.y);
  const dx = Math.max(r.x - x1, x0 - (r.x + r.w), 0);
  const dy = Math.max(r.y - y1, y0 - (r.y + r.h), 0);
  return Math.hypot(dx, dy);
}

/** collinear says two axis-aligned segments share a stretch of one line. */
function collinear(a: Point, b: Point, c: Point, d: Point): boolean {
  if (a.x === b.x && c.x === d.x && a.x === c.x) {
    const lo = Math.max(Math.min(a.y, b.y), Math.min(c.y, d.y));
    const hi = Math.min(Math.max(a.y, b.y), Math.max(c.y, d.y));
    return hi > lo;
  }
  if (a.y === b.y && c.y === d.y && a.y === c.y) {
    const lo = Math.max(Math.min(a.x, b.x), Math.min(c.x, d.x));
    const hi = Math.min(Math.max(a.x, b.x), Math.max(c.x, d.x));
    return hi > lo;
  }
  return false;
}
