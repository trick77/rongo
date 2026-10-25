import type {
  EdgeRoute,
  FlowLayout,
  LabelBox,
  NodeBox,
  Point,
  ZoneBox,
} from "./diagramLayout";
import { ramp, type Palette } from "./diagramTheme";

/**
 * A layout written as SVG, in the reference design's vocabulary: a canvas in
 * the surface colour under a hairline, boxes framed in the line colour with
 * a 6px corner, flat diamonds, an outcome in the tint, a zone as a tinted
 * frame titled in bold, arrows in the muted colour with a filled head that
 * stops just short of the box, and labels in the small mono beside their
 * line.
 *
 * A string, not elements: diagram.tsx hands every drawing on as an SVG
 * string, the renderer's and this one alike, so the card, the full view,
 * the Repos page and the file all take it unchanged. The string is built
 * here and nowhere else, and every piece of model text goes through esc, so
 * a label is text in the picture whatever characters it holds.
 *
 * Paint order: canvas, zones, arrows, labels, nodes.
 */

const esc = (s: string) =>
  s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");

type Attrs = Record<string, string | number | undefined>;

function el(tag: string, attrs: Attrs, body?: string): string {
  const a = Object.entries(attrs)
    .filter(([, v]) => v !== undefined)
    .map(([k, v]) => ` ${k}="${esc(String(v))}"`)
    .join("");
  return body === undefined ? `<${tag}${a}/>` : `<${tag}${a}>${body}</${tag}>`;
}

/** How far an arrow stops short of the box it points at, so the head
 * touches the frame rather than burying its tip in it. */
const headGap = 4;

const f = (v: number) => +v.toFixed(1);

/** shorten pulls an end of a polyline back along its last segment. */
export function shorten(
  points: Point[],
  atStart: boolean,
  by: number,
): Point[] {
  const p = points.map((q) => ({ ...q }));
  const [a, b] = atStart ? [p[0], p[1]] : [p[p.length - 1], p[p.length - 2]];
  const len = Math.hypot(a.x - b.x, a.y - b.y);
  if (len <= by) return p;
  a.x -= ((a.x - b.x) / len) * by;
  a.y -= ((a.y - b.y) / len) * by;
  return p;
}

/** drawFlow writes a flowchart layout. ids prefixes the marker id: two
 * drawings on one page must not share one. */
export function drawFlow(l: FlowLayout, pal: Palette, ids: string): string {
  // One head per arrow colour: a marker takes its fill from its own
  // definition, not from the line it ends.
  const head = (id: string, fill: string) =>
    el(
      "marker",
      {
        id: `${ids}-${id}`,
        viewBox: "0 0 10 10",
        refX: 9,
        refY: 5,
        markerWidth: 7,
        markerHeight: 7,
        orient: "auto-start-reverse",
      },
      el("path", { d: "M0,0 L10,5 L0,10 z", fill }),
    );
  const defs = el(
    "defs",
    {},
    head("arrow", pal.muted) + head("warn", pal.warn),
  );
  const body = [
    el("rect", {
      x: 0.5,
      y: 0.5,
      width: l.width - 1,
      height: l.height - 1,
      rx: 8,
      fill: pal.surface,
      stroke: pal.line,
      "stroke-width": 1,
    }),
    ...l.zones.map((z) => zone(z, pal)),
    ...l.edges.map((e) => edge(e, pal, ids)),
    ...l.edges.map((e) =>
      e.label ? label(e.label, e.warn ? pal.warn : pal.muted, pal) : "",
    ),
    ...l.nodes.map((n) => node(n, pal)),
  ].join("");
  return el(
    "svg",
    {
      xmlns: "http://www.w3.org/2000/svg",
      viewBox: `0 0 ${l.width} ${l.height}`,
      width: l.width,
      height: l.height,
      "font-family": pal.sans,
    },
    defs + body,
  );
}

function zone(z: ZoneBox, pal: Palette): string {
  return (
    el("rect", {
      x: z.x,
      y: z.y,
      width: z.w,
      height: z.h,
      rx: 10,
      fill: pal.tint,
      stroke: pal.line,
      "stroke-width": 1.2,
    }) +
    el(
      "text",
      {
        x: z.x + 18,
        y: z.y + 26,
        fill: pal.fg,
        "font-size": ramp.bold,
        "font-weight": 600,
      },
      esc(z.title),
    )
  );
}

function edge(e: EdgeRoute, pal: Palette, ids: string): string {
  let pts = e.points;
  if (e.head !== "none") pts = shorten(pts, false, headGap);
  if (e.head === "both") pts = shorten(pts, true, headGap);
  const marker = `url(#${ids}-${e.warn ? "warn" : "arrow"})`;
  return el("polyline", {
    points: pts.map((p) => `${f(p.x)},${f(p.y)}`).join(" "),
    fill: "none",
    stroke: e.warn ? pal.warn : pal.muted,
    "stroke-width": 1.4,
    "stroke-dasharray": e.dashed ? "5 4" : undefined,
    "marker-end": e.head === "none" ? undefined : marker,
    "marker-start": e.head === "both" ? marker : undefined,
  });
}

function label(m: LabelBox, fill: string, pal: Palette): string {
  const attrs = { fill, "font-family": pal.mono, "font-size": ramp.label };
  if (m.vertical) {
    const cx = m.x + m.w / 2;
    const cy = m.y + m.h / 2;
    return el(
      "text",
      {
        ...attrs,
        x: f(cx),
        y: f(cy),
        "text-anchor": "middle",
        "dominant-baseline": "central",
        transform: `rotate(-90 ${f(cx)} ${f(cy)})`,
      },
      esc(m.lines[0]),
    );
  }
  const lh = m.h / m.lines.length;
  return m.lines
    .map((line, i) =>
      el(
        "text",
        {
          ...attrs,
          x: f(m.x + m.w / 2),
          y: f(m.y + i * lh + lh / 2),
          "text-anchor": "middle",
          "dominant-baseline": "central",
        },
        esc(line),
      ),
    )
    .join("");
}

function node(n: NodeBox, pal: Palette): string {
  // The focus is framed in the accent at twice the weight, the reference's way
  // of marking the steps the page is about.
  const stroke = n.focus ? pal.accent : pal.line;
  let shape: string;
  if (n.kind === "decision") {
    const cx = n.x + n.w / 2;
    const cy = n.y + n.h / 2;
    shape = el("polygon", {
      points: `${f(cx)},${f(n.y)} ${f(n.x + n.w)},${f(cy)} ${f(cx)},${f(n.y + n.h)} ${f(n.x)},${f(cy)}`,
      fill: pal.surface,
      stroke,
      "stroke-width": n.focus ? 2 : 1.2,
    });
  } else {
    const outcome = n.kind === "outcome";
    shape = el("rect", {
      x: n.x,
      y: n.y,
      width: n.w,
      height: n.h,
      rx: 6,
      fill: outcome ? pal.tint : pal.surface,
      stroke,
      "stroke-width": n.focus ? 2 : outcome ? 1 : 1.2,
    });
  }
  // An outcome reads from its left edge, as the reference's do; every other box
  // is centred on its own axis.
  const start = n.kind === "outcome";
  const x = start ? n.x + 16 : n.x + n.w / 2;
  const first = n.y + n.h / 2 - ((n.lines.length - 1) * ramp.lineHeight) / 2;
  const text = n.lines
    .map((line, i) =>
      el(
        "text",
        {
          x: f(x),
          y: f(first + i * ramp.lineHeight),
          fill: pal.fg,
          "font-family": n.face === "mono" ? pal.mono : undefined,
          "font-size": n.size,
          "font-weight": n.bold ? 600 : undefined,
          "text-anchor": start ? "start" : "middle",
          "dominant-baseline": "central",
        },
        esc(line),
      ),
    )
    .join("");
  return shape + text;
}
