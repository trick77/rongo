import type { Point } from "./diagramLayout";
import { looksLikeCode } from "./diagramLayout";
import type { SeqHead, SequenceIR } from "./diagramIR";
import { ramp, textWidth, wrap, type Face } from "./diagramTheme";

/**
 * Where everything in a sequence goes, in the reference design's vocabulary:
 * the participants in one row of equal-height boxes at the top, a dashed
 * lifeline down from each, every message a straight line between two of
 * them with its text centred above it, a message to itself a loop out to
 * the right with its text beside it, a note on the lifelines it is about,
 * and a frame (alt, loop, par ...) a tinted box around its rows, titled at
 * its top left and split by a dashed divider at each else.
 *
 * Columns are spaced by what has to fit between them: two boxes side by
 * side, a message's text over its span, a loop's text beside it, a note
 * beside its lifeline. Rows are placed one under another in the order the
 * source wrote them, which is the order of time.
 */

export type TextBlock = {
  lines: string[];
  face: Face;
  size: number;
  x: number;
  y: number;
  w: number;
  h: number;
};
export type SeqActor = {
  id: string;
  lines: string[];
  face: Face;
  size: number;
  bold: boolean;
  x: number;
  y: number;
  w: number;
  h: number;
};
export type SeqLifeline = { id: string; x: number; y1: number; y2: number };
export type SeqMessage = {
  from: string;
  to: string;
  points: Point[];
  dashed: boolean;
  head: SeqHead;
  label?: TextBlock;
  self: boolean;
  /** Inside a frame, so its text sits on the frame's tint. */
  framed: boolean;
};
export type SeqNote = {
  lines: string[];
  /** The lifeline a note left or right of one sits beside. */
  beside?: string;
  x: number;
  y: number;
  w: number;
  h: number;
};
export type SeqFrame = {
  keyword: string;
  /** The condition, wrapped; its lines sit beside the keyword. */
  cond: string[];
  x: number;
  y: number;
  w: number;
  h: number;
  /** The title's box: the keyword, then the condition. */
  title: { x: number; y: number; w: number; h: number };
  dividers: { y: number; label?: TextBlock }[];
};
export type SequenceLayout = {
  width: number;
  height: number;
  actors: SeqActor[];
  lifelines: SeqLifeline[];
  messages: SeqMessage[];
  notes: SeqNote[];
  frames: SeqFrame[];
};

/** Space around the drawing, inside the canvas. */
const margin = 24;
/** Clear canvas above the participants. The card's full-view and file
 * buttons sit in the canvas's top right corner, where a flow leaves margin
 * and a sequence puts its last participant; at the scale a five-participant
 * sequence is drawn inline they covered that box's name. */
const toolsBand = 32;
/** Text to frame, each side, in a participant box. */
export const boxPad = 16;
const boxMin = 96;
/** The widest a participant's name runs before it wraps. */
const nameMax = 160;
/** Clear space between two participant boxes. */
const colGap = 24;
/** A message's text: one line, and the widest before it wraps. */
const textLine = 16;
const textMax = 180;
/** Clear space either side of a message's text, inside its arrow. */
const textPad = 16;
/** Between a message's text and its line. */
const textGap = 8;
/** Between one row and the next. */
const rowGap = 18;
/** A loop to itself: out this far, down at least this far. */
const selfW = 24;
const selfH = 18;
const selfGap = 8;
/** A note: text to frame, the widest before it wraps, the least width. */
export const notePad = 12;
const noteMax = 240;
const noteMin = 80;
/** Between a note and the lifeline it sits beside. */
const noteGap = 12;
/** A note over two lifelines reaches this far past both. */
const noteReach = 24;
/** A frame: past its outermost lifeline, its title band, title inset. */
const framePad = 16;
const titleInset = 12;
/** Above and below a frame's title, inside the frame. */
const titlePad = 8;
/** A frame's condition: one line, and the widest before it wraps. */
const condLine = 14;
const condMax = 220;
/** Least space between a divider and the text above it. */
const dividerGap = 6;

const up = (v: number) => Math.ceil(v);

type Style = { face: Face; size: number; lineH: number };

/** A message's text: a name from the code in the mono, else the sans. */
function messageStyle(label: string): Style {
  return looksLikeCode(label)
    ? { face: "mono", size: ramp.code, lineH: textLine }
    : { face: "sans", size: ramp.text, lineH: textLine };
}
const noteStyle: Style = {
  face: "sans",
  size: ramp.text,
  lineH: ramp.lineHeight,
};
/** A frame's or a divider's condition: the small mono, as a flow's arrow
 * label. */
const condStyle: Style = { face: "mono", size: ramp.label, lineH: condLine };

/** text is a label wrapped at max and measured, not yet placed. */
function text(
  label: string,
  max: number,
  s: Style,
): Omit<TextBlock, "x" | "y"> {
  const lines = wrap(label, max, s.size, s.face);
  const w = up(Math.max(...lines.map((l) => textWidth(l, s.size, s.face))));
  return { lines, face: s.face, size: s.size, w, h: lines.length * s.lineH };
}

/** The room between a frame's keyword and its condition. */
export const keywordGap = textWidth("  ", ramp.label, "mono");

type Constraint = { a: number; b: number; need: number };

export function layoutSequence(ir: SequenceIR): SequenceLayout {
  const col = new Map(ir.actors.map((a, i) => [a.id, i]));
  const n = ir.actors.length;

  // ---- the participants ----
  const heads = ir.actors.map((a) => {
    const code = looksLikeCode(a.label);
    const face: Face = code ? "mono" : "sans";
    const size = code ? ramp.code : ramp.bold;
    const lines = wrap(a.label, nameMax, size, face);
    const tw = Math.max(...lines.map((l) => textWidth(l, size, face)));
    return {
      a,
      face,
      size,
      bold: !code,
      lines,
      w: Math.max(boxMin, up(tw + 2 * boxPad)),
    };
  });
  const headH =
    Math.max(...heads.map((h) => h.lines.length)) * ramp.lineHeight + 22;

  // ---- what each row needs, before anything is placed ----
  const texts = ir.rows.map((r) =>
    r.type === "message" && r.label !== ""
      ? text(r.label, textMax, messageStyle(r.label))
      : r.type === "note"
        ? text(r.label, noteMax, noteStyle)
        : null,
  );
  const cons: Constraint[] = [];
  for (let i = 0; i + 1 < n; i++)
    cons.push({
      a: i,
      b: i + 1,
      need: heads[i].w / 2 + heads[i + 1].w / 2 + colGap,
    });
  ir.rows.forEach((r, k) => {
    const t = texts[k];
    if (r.type === "message") {
      const i = col.get(r.from)!;
      const j = col.get(r.to)!;
      if (i === j) {
        if (i + 1 < n)
          cons.push({
            a: i,
            b: i + 1,
            need: selfW + selfGap + (t?.w ?? 0) + textPad,
          });
      } else {
        cons.push({
          a: Math.min(i, j),
          b: Math.max(i, j),
          need: (t?.w ?? 0) + 2 * textPad,
        });
      }
    } else if (r.type === "note" && t) {
      const i = col.get(r.from)!;
      const w = Math.max(noteMin, t.w + 2 * notePad);
      if (r.side === "right" && i + 1 < n)
        cons.push({ a: i, b: i + 1, need: 2 * noteGap + w });
      if (r.side === "left" && i > 0)
        cons.push({ a: i - 1, b: i, need: 2 * noteGap + w });
    }
  });
  const xs = columns(n, cons);

  // ---- the rows, top to bottom ----
  const messages: SeqMessage[] = [];
  const notes: SeqNote[] = [];
  const frames: SeqFrame[] = [];
  /** floor: the highest a divider may sit, clear of the title or the
   * divider text above it when no row came between. */
  type Open = { f: SeqFrame; cols: Set<number>; boxes: Box[]; floor: number };
  let open: Open | null = null;
  const top = margin + toolsBand + headH;
  let y = top + 24;

  ir.rows.forEach((r, k) => {
    const t = texts[k];
    if (r.type === "message") {
      const i = col.get(r.from)!;
      const j = col.get(r.to)!;
      const base = {
        from: r.from,
        to: r.to,
        dashed: r.line === "dashed",
        head: r.head,
        framed: open !== null,
      };
      let m: SeqMessage;
      if (i === j) {
        const x = xs[i];
        const h = Math.max(selfH, t?.h ?? 0);
        const label = t && {
          ...t,
          x: x + selfW + selfGap,
          y: y + h / 2 - t.h / 2,
        };
        m = {
          ...base,
          points: [
            { x, y },
            { x: x + selfW, y },
            { x: x + selfW, y: y + h },
            { x, y: y + h },
          ],
          self: true,
          ...(label ? { label } : {}),
        };
        y += h + rowGap;
      } else {
        const lineY = t ? y + t.h + textGap : y + 6;
        const label = t && { ...t, x: (xs[i] + xs[j]) / 2 - t.w / 2, y };
        m = {
          ...base,
          points: [
            { x: xs[i], y: lineY },
            { x: xs[j], y: lineY },
          ],
          self: false,
          ...(label ? { label } : {}),
        };
        y = lineY + rowGap;
      }
      messages.push(m);
      if (open) {
        open.cols.add(i).add(j);
        if (m.label) open.boxes.push(m.label);
        if (m.self) open.boxes.push({ x: xs[i], y: 0, w: selfW, h: 0 });
      }
    } else if (r.type === "note") {
      const i = col.get(r.from)!;
      const j = col.get(r.to)!;
      const tw = t ?? text("", noteMax, noteStyle);
      let w = Math.max(noteMin, tw.w + 2 * notePad);
      const h = tw.lines.length * ramp.lineHeight + 14;
      let x: number;
      if (r.side === "left") x = xs[i] - noteGap - w;
      else if (r.side === "right") x = xs[i] + noteGap;
      else {
        const lo = Math.min(xs[i], xs[j]);
        const hi = Math.max(xs[i], xs[j]);
        if (i !== j) w = Math.max(w, hi - lo + 2 * noteReach);
        x = (lo + hi) / 2 - w / 2;
      }
      const note: SeqNote = { lines: tw.lines, x, y, w, h };
      if (r.side !== "over") note.beside = r.from;
      notes.push(note);
      if (open) {
        open.cols.add(i).add(j);
        open.boxes.push(note);
      }
      y += h + rowGap;
    } else if (r.type === "frame") {
      // The keyword, then the condition wrapped beside it: a long
      // condition left whole widened the frame, and with it the canvas
      // every other label is scaled down in.
      const kw = up(textWidth(r.keyword, ramp.label, "mono"));
      const cond =
        r.label === "" ? [] : text(r.label, condMax, condStyle).lines;
      const condW = Math.max(
        0,
        ...cond.map((l) => textWidth(l, ramp.label, "mono")),
      );
      const h = Math.max(1, cond.length) * condLine;
      const f: SeqFrame = {
        keyword: r.keyword,
        cond,
        x: 0,
        y,
        w: 0,
        h: 0,
        title: {
          x: 0,
          y: y + titlePad,
          w: up(cond.length > 0 ? kw + keywordGap + condW : kw),
          h,
        },
        dividers: [],
      };
      frames.push(f);
      const floor = f.title.y + h + dividerGap;
      open = { f, cols: new Set(), boxes: [], floor };
      y += 2 * titlePad + h;
    } else if (r.type === "divider" && open) {
      const lineY = Math.max(y - rowGap / 2, open.floor);
      if (r.label !== "") {
        const lt = text(r.label, condMax, condStyle);
        const label = { ...lt, x: 0, y: lineY + dividerGap };
        open.f.dividers.push({ y: lineY, label });
        open.floor = label.y + lt.h + dividerGap;
        y = label.y + lt.h + 12;
      } else {
        open.f.dividers.push({ y: lineY });
        open.floor = lineY + dividerGap;
        y = lineY + 12;
      }
    } else if (r.type === "end" && open) {
      const f = open.f;
      f.h = y - rowGap / 2 + 6 - f.y;
      const cols = open.cols.size > 0 ? [...open.cols] : xs.map((_, i) => i);
      let x0 = Math.min(...cols.map((c) => xs[c])) - framePad;
      let x1 = Math.max(...cols.map((c) => xs[c])) + framePad;
      for (const b of open.boxes) {
        x0 = Math.min(x0, b.x - titleInset);
        x1 = Math.max(x1, b.x + b.w + titleInset);
      }
      for (const d of f.dividers)
        if (d.label)
          x1 = Math.max(x1, x0 + titleInset + d.label.w + titleInset);
      x1 = Math.max(x1, x0 + titleInset + f.title.w + titleInset);
      f.x = x0;
      f.w = x1 - x0;
      f.title.x = x0 + titleInset;
      for (const d of f.dividers) if (d.label) d.label.x = x0 + titleInset;
      open = null;
      y = f.y + f.h + rowGap;
    }
  });

  const bottom = y - rowGap + 16;
  const actors: SeqActor[] = heads.map((h, i) => ({
    id: h.a.id,
    lines: h.lines,
    face: h.face,
    size: h.size,
    bold: h.bold,
    x: xs[i] - h.w / 2,
    y: margin + toolsBand,
    w: h.w,
    h: headH,
  }));
  const lifelines = ir.actors.map((a, i) => ({
    id: a.id,
    x: xs[i],
    y1: top,
    y2: bottom,
  }));

  // Everything was placed from the first lifeline at 0; a note left of it
  // or a loop right of the last reaches past, so the canvas is the extent
  // of what was drawn, and every x moves by one shift.
  const boxes: Box[] = [
    ...actors,
    ...notes,
    ...frames,
    ...frames.map((f) => f.title),
    ...messages.flatMap((m) => (m.label ? [m.label] : [])),
    ...messages
      .filter((m) => m.self)
      .map((m) => ({ x: m.points[1].x, y: 0, w: 0, h: 0 })),
  ];
  const minX = Math.min(...boxes.map((b) => b.x));
  const maxX = Math.max(...boxes.map((b) => b.x + b.w));
  const dx = margin - minX;
  const move = (b: { x: number }) => {
    b.x += dx;
  };
  actors.forEach(move);
  notes.forEach(move);
  lifelines.forEach(move);
  for (const f of frames) {
    move(f);
    move(f.title);
    for (const d of f.dividers) if (d.label) move(d.label);
  }
  for (const m of messages) {
    m.points.forEach(move);
    if (m.label) move(m.label);
  }
  return {
    width: up(maxX + dx + margin),
    height: up(bottom + margin),
    actors,
    lifelines,
    messages,
    notes,
    frames,
  };
}

type Box = { x: number; y: number; w: number; h: number };

/** columns is each lifeline's x, the first at 0. Every constraint says two
 * lifelines stand at least so far apart; the narrowest are met first, and a
 * wider one short of room shares what it lacks evenly over the gaps it
 * spans, so a long message over three lifelines widens both gaps alike. */
function columns(n: number, cons: Constraint[]): number[] {
  const gap = Array.from({ length: Math.max(0, n - 1) }, () => 0);
  const sorted = [...cons].sort((p, q) => p.b - p.a - (q.b - q.a));
  for (const c of sorted) {
    let have = 0;
    for (let i = c.a; i < c.b; i++) have += gap[i];
    if (have >= c.need) continue;
    const add = (c.need - have) / (c.b - c.a);
    for (let i = c.a; i < c.b; i++) gap[i] += add;
  }
  const xs = [0];
  for (const g of gap) xs.push(xs[xs.length - 1] + up(g));
  return xs;
}
