import type {
  EdgeRoute,
  FlowLayout,
  LabelBox,
  NodeBox,
  Point,
  ZoneBox,
} from "./diagramLayout";
import {
  keywordGap,
  notePad,
  type SeqActor,
  type SeqFrame,
  type SeqMessage,
  type SeqNote,
  type SequenceLayout,
  type TextBlock,
} from "./diagramSequence";
import { ramp, textWidth, type Palette } from "./diagramTheme";
import type { Card } from "./diagramIR";
import type { ERLayout, Table } from "./diagramER";

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
/** How far a sequence's arrow stops short of its lifeline: the head's tip
 * overshoots the line's end by about this much at a 1.4 stroke. */
const tipGap = 1;

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
  const defs =
    marker(ids, "arrow", filledHead, { fill: pal.muted }) +
    marker(ids, "warn", filledHead, { fill: pal.warn });
  return svg(l.width, l.height, pal, defs, [
    ...l.zones.map((z) => zone(z, pal)),
    ...l.edges.map((e) => edge(e, pal, ids)),
    ...l.edges.map((e) =>
      e.label ? label(e.label, e.warn ? pal.warn : pal.muted, pal) : "",
    ),
    ...l.nodes.map((n) => node(n, pal)),
  ]);
}

const filledHead = "M0,0 L10,5 L0,10 z";

/** marker is an arrowhead definition, its tip at refX on the line's end. */
function marker(
  ids: string,
  id: string,
  d: string,
  paint: Attrs,
  refX = 9,
): string {
  return el(
    "marker",
    {
      id: `${ids}-${id}`,
      viewBox: "0 0 10 10",
      refX,
      refY: 5,
      markerWidth: 7,
      markerHeight: 7,
      orient: "auto-start-reverse",
    },
    el("path", { d, ...paint }),
  );
}

/** svg is a drawing's root: its own size, its marker definitions, and the
 * canvas under everything drawn on it. */
function svg(
  width: number,
  height: number,
  pal: Palette,
  defs: string,
  body: string[],
): string {
  const canvas = el("rect", {
    x: 0.5,
    y: 0.5,
    width: width - 1,
    height: height - 1,
    rx: 8,
    fill: pal.surface,
    stroke: pal.line,
    "stroke-width": 1,
  });
  return el(
    "svg",
    {
      xmlns: "http://www.w3.org/2000/svg",
      viewBox: `0 0 ${width} ${height}`,
      width,
      height,
      "font-family": pal.sans,
    },
    el("defs", {}, defs) + canvas + body.join(""),
  );
}

/** drawSequence writes a sequence layout in the same vocabulary: boxes
 * framed in the line colour, lifelines a fine dash of it, messages in the
 * muted colour, a reply dashed, a frame a tinted box titled in the small
 * mono. No accent and no amber: the source has no focus to give, and a
 * dashed line here is a reply, not a boundary.
 *
 * Paint order: canvas, frames, lifelines, participants, notes, arrows, then
 * every text on a patch of its own ground, so a lifeline a message crosses
 * never strikes through its words. */
export function drawSequence(
  l: SequenceLayout,
  pal: Palette,
  ids: string,
): string {
  const line = { fill: "none", stroke: pal.muted, "stroke-width": 1.6 };
  const defs =
    marker(ids, "arrow", filledHead, { fill: pal.muted }) +
    marker(ids, "open", "M1,1 L9,5 L1,9", line) +
    marker(ids, "cross", "M2,2 L8,8 M8,2 L2,8", line, 5);
  return svg(l.width, l.height, pal, defs, [
    ...l.frames.map((fr) => frame(fr, pal)),
    ...l.lifelines.map((ln) =>
      el("line", {
        x1: f(ln.x),
        y1: f(ln.y1),
        x2: f(ln.x),
        y2: f(ln.y2),
        stroke: pal.line,
        "stroke-width": 1,
        "stroke-dasharray": "4 4",
      }),
    ),
    ...l.actors.map((a) => actor(a, pal)),
    ...l.notes.map((n) => note(n, pal)),
    ...l.messages.map((m) => message(m, pal, ids)),
    ...l.frames.map((fr) => frameText(fr, pal)),
    ...l.messages.map((m) =>
      m.label
        ? block(m.label, m.framed ? pal.tint : pal.surface, m.self, pal.fg, pal)
        : "",
    ),
  ]);
}

/** drawER writes an entity model: tables framed as boxes are, their name
 * in a tinted head, fields in the mono with types and keys in the muted;
 * relationships in the muted line, a non-identifying one dashed (never
 * coloured: here a dash is a kind of relationship, not a boundary), each
 * end marked with its cardinality, crow's foot for many, bar for one,
 * ring for zero.
 *
 * Paint order: canvas, lines, marks, roles, tables. The marks are drawn,
 * not markers, so no id is needed. */
export function drawER(l: ERLayout, pal: Palette): string {
  return svg(l.width, l.height, pal, "", [
    ...l.edges.map((e) =>
      el("polyline", {
        points: e.points.map((p) => `${f(p.x)},${f(p.y)}`).join(" "),
        fill: "none",
        stroke: pal.muted,
        "stroke-width": 1.4,
        "stroke-dasharray": e.dashed ? "5 4" : undefined,
      }),
    ),
    ...l.edges.map((e, i) =>
      e.points.length < 2
        ? ""
        : mark(e.points[0], e.points[1], l.cards[i].from, pal) +
          mark(
            e.points[e.points.length - 1],
            e.points[e.points.length - 2],
            l.cards[i].to,
            pal,
          ),
    ),
    ...l.edges.map((e) => (e.label ? label(e.label, pal.muted, pal) : "")),
    ...l.nodes.map((n) => entity(n, l.tables.get(n.id)!, pal)),
  ]);
}

/** mark is a cardinality at the end of a line that meets a table at end,
 * the line running on towards next: bars for exactly one, a ring for zero,
 * a crow's foot spread against the table for many. */
function mark(end: Point, next: Point, card: Card, pal: Palette): string {
  const len = Math.hypot(next.x - end.x, next.y - end.y) || 1;
  const u = { x: (next.x - end.x) / len, y: (next.y - end.y) / len };
  const v = { x: -u.y, y: u.x };
  const at = (s: number, t = 0) => ({
    x: end.x + u.x * s + v.x * t,
    y: end.y + u.y * s + v.y * t,
  });
  const stroke = { stroke: pal.muted, "stroke-width": 1.4, fill: "none" };
  const seg = (a: Point, b: Point) =>
    el("line", { x1: f(a.x), y1: f(a.y), x2: f(b.x), y2: f(b.y), ...stroke });
  const bar = (s: number) => seg(at(s, -6), at(s, 6));
  const ring = (s: number) => {
    const c = at(s);
    return el("circle", {
      cx: f(c.x),
      cy: f(c.y),
      r: 4,
      fill: pal.surface,
      stroke: pal.muted,
      "stroke-width": 1.2,
    });
  };
  const foot = () => seg(at(12), at(0, -7)) + seg(at(12), at(0, 7));
  switch (card) {
    case "one":
      return bar(8) + bar(13);
    case "zeroOrOne":
      return bar(8) + ring(17);
    case "oneOrMore":
      return foot() + bar(16);
    case "zeroOrMore":
      return foot() + ring(18);
  }
}

/** entity is a table: the frame of a box, a head in the tint under a
 * hairline, and a row per field, the columns as the layout lined them up. */
function entity(n: NodeBox, t: Table, pal: Palette): string {
  const stroke = n.focus ? pal.accent : pal.line;
  const width = n.focus ? 2 : 1.2;
  const r = 6;
  const head =
    t.rows.length === 0
      ? ""
      : el("path", {
          d: `M${f(n.x)},${f(n.y + t.head)} V${f(n.y + r)} Q${f(n.x)},${f(n.y)} ${f(n.x + r)},${f(n.y)} H${f(n.x + n.w - r)} Q${f(n.x + n.w)},${f(n.y)} ${f(n.x + n.w)},${f(n.y + r)} V${f(n.y + t.head)} Z`,
          fill: pal.tint,
        }) +
        el("line", {
          x1: f(n.x),
          y1: f(n.y + t.head),
          x2: f(n.x + n.w),
          y2: f(n.y + t.head),
          stroke: pal.line,
          "stroke-width": 1,
        });
  const name = el(
    "text",
    {
      x: f(n.x + 16),
      y: f(n.y + t.head / 2),
      fill: pal.fg,
      "font-family": t.name.face === "mono" ? pal.mono : undefined,
      "font-size": t.name.size,
      "font-weight": t.name.bold ? 600 : undefined,
      "dominant-baseline": "central",
    },
    esc(t.name.text),
  );
  const rows = t.rows
    .map((row) =>
      row.cells
        .map((c) =>
          c.text
            .map((line, i) =>
              el(
                "text",
                {
                  x: f(n.x + c.x),
                  y: f(n.y + row.y + 3 + i * 16 + 8),
                  fill: c.muted ? pal.muted : pal.fg,
                  "font-family": c.face === "mono" ? pal.mono : undefined,
                  "font-size": c.size,
                  "dominant-baseline": "central",
                },
                esc(line),
              ),
            )
            .join(""),
        )
        .join(""),
    )
    .join("");
  // The frame last, over the head's fill, so its corners stay crisp.
  const box = el("rect", {
    x: n.x,
    y: n.y,
    width: n.w,
    height: n.h,
    rx: r,
    fill: "none",
    stroke,
    "stroke-width": width,
  });
  const ground = el("rect", {
    x: n.x,
    y: n.y,
    width: n.w,
    height: n.h,
    rx: r,
    fill: pal.surface,
  });
  return ground + head + name + rows + box;
}

function frame(fr: SeqFrame, pal: Palette): string {
  return (
    el("rect", {
      x: f(fr.x),
      y: f(fr.y),
      width: f(fr.w),
      height: f(fr.h),
      rx: 10,
      fill: pal.tint,
      stroke: pal.line,
      "stroke-width": 1.2,
    }) +
    fr.dividers
      .map((d) =>
        el("line", {
          x1: f(fr.x),
          y1: f(d.y),
          x2: f(fr.x + fr.w),
          y2: f(d.y),
          stroke: pal.line,
          "stroke-width": 1,
          "stroke-dasharray": "4 4",
        }),
      )
      .join("")
  );
}

/** frameText is a frame's title and its dividers' conditions, each on a
 * patch of the frame's tint: a lifeline runs through the title's corner. */
function frameText(fr: SeqFrame, pal: Palette): string {
  const t = fr.title;
  const mono = {
    "font-family": pal.mono,
    "font-size": ramp.label,
    "dominant-baseline": "central",
  };
  const lh = t.h / Math.max(1, fr.cond.length);
  // The condition's lines all start where its first did, beside the
  // keyword, so a wrapped one reads as one block.
  const condX = t.x + textWidth(fr.keyword, ramp.label, "mono") + keywordGap;
  const title =
    knockout(t, pal.tint) +
    el(
      "text",
      {
        ...mono,
        x: f(t.x),
        y: f(t.y + lh / 2),
        fill: pal.fg,
        "font-weight": 600,
      },
      esc(fr.keyword),
    ) +
    fr.cond
      .map((line, i) =>
        el(
          "text",
          {
            ...mono,
            x: f(condX),
            y: f(t.y + i * lh + lh / 2),
            fill: pal.muted,
          },
          esc(line),
        ),
      )
      .join("");
  const conds = fr.dividers
    .map((d) => (d.label ? block(d.label, pal.tint, true, pal.muted, pal) : ""))
    .join("");
  return title + conds;
}

function knockout(
  r: { x: number; y: number; w: number; h: number },
  fill: string,
): string {
  return el("rect", {
    x: f(r.x - 3),
    y: f(r.y - 1),
    width: f(r.w + 6),
    height: f(r.h + 2),
    fill,
  });
}

function actor(a: SeqActor, pal: Palette): string {
  const first = a.y + a.h / 2 - ((a.lines.length - 1) * ramp.lineHeight) / 2;
  return (
    el("rect", {
      x: f(a.x),
      y: f(a.y),
      width: f(a.w),
      height: f(a.h),
      rx: 6,
      fill: pal.surface,
      stroke: pal.line,
      "stroke-width": 1.2,
    }) +
    a.lines
      .map((line, i) =>
        el(
          "text",
          {
            x: f(a.x + a.w / 2),
            y: f(first + i * ramp.lineHeight),
            fill: pal.fg,
            "font-family": a.face === "mono" ? pal.mono : undefined,
            "font-size": a.size,
            "font-weight": a.bold ? 600 : undefined,
            "text-anchor": "middle",
            "dominant-baseline": "central",
          },
          esc(line),
        ),
      )
      .join("")
  );
}

function note(n: SeqNote, pal: Palette): string {
  const first = n.y + n.h / 2 - ((n.lines.length - 1) * ramp.lineHeight) / 2;
  return (
    el("rect", {
      x: f(n.x),
      y: f(n.y),
      width: f(n.w),
      height: f(n.h),
      rx: 6,
      fill: pal.tint,
      stroke: pal.line,
      "stroke-width": 1,
    }) +
    n.lines
      .map((line, i) =>
        el(
          "text",
          {
            x: f(n.x + notePad),
            y: f(first + i * ramp.lineHeight),
            fill: pal.fg,
            "font-size": ramp.text,
            "dominant-baseline": "central",
          },
          esc(line),
        ),
      )
      .join("")
  );
}

function message(m: SeqMessage, pal: Palette, ids: string): string {
  // A message ends on a lifeline, a hairline, not a box frame: the tip
  // touches it. Only the filled and open heads reach past their line's
  // end; a cross is centred on it.
  let pts = m.points;
  if (m.head === "filled" || m.head === "open" || m.head === "both")
    pts = shorten(pts, false, tipGap);
  if (m.head === "both") pts = shorten(pts, true, tipGap);
  const end =
    m.head === "open" ? "open" : m.head === "cross" ? "cross" : "arrow";
  return el("polyline", {
    points: pts.map((p) => `${f(p.x)},${f(p.y)}`).join(" "),
    fill: "none",
    stroke: pal.muted,
    "stroke-width": 1.4,
    "stroke-dasharray": m.dashed ? "5 4" : undefined,
    "marker-end": m.head === "none" ? undefined : `url(#${ids}-${end})`,
    "marker-start": m.head === "both" ? `url(#${ids}-arrow)` : undefined,
  });
}

/** block is a text on a patch of its ground: centred over its arrow, or
 * read from the left beside a loop or under a divider. */
function block(
  t: TextBlock,
  ground: string,
  start: boolean,
  fill: string,
  pal: Palette,
): string {
  const lh = t.h / t.lines.length;
  return (
    knockout(t, ground) +
    t.lines
      .map((line, i) =>
        el(
          "text",
          {
            x: f(start ? t.x : t.x + t.w / 2),
            y: f(t.y + i * lh + lh / 2),
            fill,
            "font-family": t.face === "mono" ? pal.mono : undefined,
            "font-size": t.size,
            "text-anchor": start ? "start" : "middle",
            "dominant-baseline": "central",
          },
          esc(line),
        ),
      )
      .join("")
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
  // A state diagram's `[*]`: where it begins a dot in the text colour,
  // where it ends the same dot in a ring. No text and no focus to carry.
  if (n.kind === "start" || n.kind === "stop") {
    const cx = f(n.x + n.w / 2);
    const cy = f(n.y + n.h / 2);
    const r = n.w / 2;
    if (n.kind === "start") return el("circle", { cx, cy, r, fill: pal.fg });
    return (
      el("circle", {
        cx,
        cy,
        r: r - 0.7,
        fill: pal.surface,
        stroke: pal.fg,
        "stroke-width": 1.4,
      }) + el("circle", { cx, cy, r: r - 5, fill: pal.fg })
    );
  }
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
