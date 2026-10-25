import { describe, it, expect } from "vitest";
import { layoutSequence } from "./diagramSequence";
import { checkSequence } from "./diagramCheck";
import type { SeqRow, SequenceIR } from "./diagramIR";
import { textWidth } from "./diagramTheme";

const actors = (...ids: string[]) => ids.map((id) => ({ id, label: id }));
const msg = (
  from: string,
  to: string,
  label: string,
  over: Partial<Extract<SeqRow, { type: "message" }>> = {},
): SeqRow => ({
  type: "message",
  from,
  to,
  label,
  line: "solid",
  head: "filled",
  ...over,
});
const seq = (a: SequenceIR["actors"], rows: SeqRow[]): SequenceIR => ({
  kind: "sequence",
  actors: a,
  rows,
});
const lifeline = (l: ReturnType<typeof layoutSequence>, id: string) =>
  l.lifelines.find((x) => x.id === id)!.x;

describe("layoutSequence", () => {
  it("puts participants in a row at the top, in source order, one height", () => {
    const l = layoutSequence(
      seq(
        [
          { id: "a", label: "Short" },
          { id: "b", label: "A participant with a name long enough to wrap" },
        ],
        [msg("a", "b", "go")],
      ),
    );
    expect(l.actors.map((a) => a.id)).toEqual(["a", "b"]);
    expect(l.actors[0].y).toBe(l.actors[1].y);
    expect(l.actors[0].h).toBe(l.actors[1].h);
    expect(l.actors[1].lines.length).toBeGreaterThan(1);
    expect(l.actors[0].x + l.actors[0].w).toBeLessThan(l.actors[1].x);
    expect(checkSequence(l)).toEqual([]);
  });

  it("draws every message as a straight line between two lifelines, top to bottom", () => {
    const l = layoutSequence(
      seq(actors("a", "b", "c"), [
        msg("a", "b", "one"),
        msg("b", "c", "two"),
        msg("c", "a", "back", { line: "dashed" }),
      ]),
    );
    const ys = l.messages.map((m) => m.points[0].y);
    expect(ys).toEqual([...ys].sort((p, q) => p - q));
    for (const m of l.messages) {
      expect(m.points).toHaveLength(2);
      expect(m.points[0].y).toBe(m.points[1].y);
    }
    expect(l.messages[2].points[0].x).toBe(lifeline(l, "c"));
    expect(l.messages[2].points[1].x).toBe(lifeline(l, "a"));
    expect(l.messages[2].dashed).toBe(true);
    expect(checkSequence(l)).toEqual([]);
  });

  it("spreads two lifelines apart until a long message fits between them", () => {
    const short = layoutSequence(seq(actors("a", "b"), [msg("a", "b", "x")]));
    const long = layoutSequence(
      seq(actors("a", "b"), [msg("a", "b", "Warenkorbartikel abrufen jetzt")]),
    );
    const gap = (l: typeof short) => lifeline(l, "b") - lifeline(l, "a");
    expect(gap(long)).toBeGreaterThan(gap(short));
    const label = long.messages[0].label!;
    expect(label.x).toBeGreaterThan(lifeline(long, "a"));
    expect(label.x + label.w).toBeLessThan(lifeline(long, "b"));
    expect(checkSequence(long)).toEqual([]);
  });

  it("shares a wide message spanning three lifelines over both gaps", () => {
    const l = layoutSequence(
      seq(actors("a", "b", "c"), [
        msg("a", "c", "a message long enough to need more room than two boxes"),
      ]),
    );
    const ab = lifeline(l, "b") - lifeline(l, "a");
    const bc = lifeline(l, "c") - lifeline(l, "b");
    expect(Math.abs(ab - bc)).toBeLessThan(1);
    expect(checkSequence(l)).toEqual([]);
  });

  it("loops a message to itself out to the right, its text beside it", () => {
    const l = layoutSequence(
      seq(actors("a", "b"), [msg("a", "a", "Bestellung speichern")]),
    );
    const m = l.messages[0];
    expect(m.self).toBe(true);
    expect(m.points).toHaveLength(4);
    expect(m.points[0].x).toBe(lifeline(l, "a"));
    expect(m.points[3].x).toBe(lifeline(l, "a"));
    expect(m.label!.x).toBeGreaterThan(m.points[1].x);
    expect(m.label!.x + m.label!.w).toBeLessThan(lifeline(l, "b"));
    expect(checkSequence(l)).toEqual([]);
  });

  it("grows the canvas for a self message on the last lifeline", () => {
    const l = layoutSequence(
      seq(actors("a", "b"), [msg("b", "b", "a long note to itself")]),
    );
    const label = l.messages[0].label!;
    expect(label.x + label.w).toBeLessThanOrEqual(l.width - 24);
    expect(checkSequence(l)).toEqual([]);
  });

  it("puts a note over one or two lifelines, or beside one", () => {
    const l = layoutSequence(
      seq(actors("a", "b"), [
        { type: "note", from: "a", to: "b", side: "over", label: "both" },
        { type: "note", from: "a", to: "a", side: "left", label: "left" },
        { type: "note", from: "b", to: "b", side: "right", label: "right" },
        { type: "note", from: "a", to: "a", side: "over", label: "one" },
      ]),
    );
    const [both, left, right, one] = l.notes;
    const a = lifeline(l, "a");
    const b = lifeline(l, "b");
    expect(both.x).toBeLessThan(a);
    expect(both.x + both.w).toBeGreaterThan(b);
    expect(left.x + left.w).toBeLessThan(a);
    expect(right.x).toBeGreaterThan(b);
    expect(one.x).toBeLessThan(a);
    expect(one.x + one.w).toBeGreaterThan(a);
    expect(checkSequence(l)).toEqual([]);
  });

  it("frames its rows, titled, split by a divider, under the lifelines", () => {
    const l = layoutSequence(
      seq(actors("a", "b", "c"), [
        msg("a", "b", "before"),
        { type: "frame", keyword: "alt", label: "Zahlung abgelehnt" },
        msg("a", "b", "no"),
        { type: "divider", keyword: "else", label: "Zahlung genehmigt" },
        msg("a", "b", "yes"),
        { type: "end" },
        msg("b", "c", "after"),
      ]),
    );
    expect(l.frames).toHaveLength(1);
    const f = l.frames[0];
    const [before, no, yes, after] = l.messages;
    expect(before.points[0].y).toBeLessThan(f.y);
    expect(no.points[0].y).toBeGreaterThan(f.y);
    expect(yes.points[0].y).toBeLessThan(f.y + f.h);
    expect(after.points[0].y).toBeGreaterThan(f.y + f.h);
    expect(f.x).toBeLessThan(lifeline(l, "a"));
    expect(f.x + f.w).toBeGreaterThan(lifeline(l, "b"));
    expect(f.dividers).toHaveLength(1);
    const d = f.dividers[0];
    expect(d.y).toBeGreaterThan(no.points[0].y);
    expect(d.y).toBeLessThan(yes.points[0].y);
    expect(no.framed && yes.framed && !before.framed).toBe(true);
    expect(f.title.w).toBeGreaterThan(
      textWidth("Zahlung abgelehnt", 11.5, "mono"),
    );
    expect(checkSequence(l)).toEqual([]);
  });

  it("widens a frame to its title", () => {
    const l = layoutSequence(
      seq(actors("a", "b"), [
        {
          type: "frame",
          keyword: "loop",
          label: "every cycle until the remote stops answering for good",
        },
        msg("a", "b", "x"),
        { type: "end" },
      ]),
    );
    const f = l.frames[0];
    expect(f.title.x + f.title.w).toBeLessThanOrEqual(f.x + f.w);
    expect(checkSequence(l)).toEqual([]);
  });

  it("runs every lifeline from its box to below the last row", () => {
    const l = layoutSequence(
      seq(actors("a", "b"), [msg("a", "b", "x"), msg("b", "a", "y")]),
    );
    for (const ln of l.lifelines) {
      expect(ln.y1).toBe(l.actors[0].y + l.actors[0].h);
      expect(ln.y2).toBeGreaterThan(l.messages[1].points[0].y);
      expect(ln.y2).toBeLessThan(l.height);
    }
  });

  it("leaves the canvas's top right corner clear for the card's buttons", () => {
    const l = layoutSequence(seq(actors("a", "b"), [msg("a", "b", "x")]));
    for (const a of l.actors) expect(a.y).toBeGreaterThanOrEqual(56);
  });

  it("sets a participant named like code in the mono", () => {
    const l = layoutSequence(
      seq(
        [
          { id: "u", label: "Ask.tsx" },
          { id: "s", label: "Server" },
        ],
        [],
      ),
    );
    expect(l.actors.map((a) => a.face)).toEqual(["mono", "sans"]);
    expect(l.actors[1].bold).toBe(true);
  });
});

describe("checkSequence", () => {
  it("says when a label sits on another or a box overflows", () => {
    const l = layoutSequence(
      seq(actors("a", "b"), [msg("a", "b", "one"), msg("a", "b", "two")]),
    );
    l.messages[1].label!.y = l.messages[0].label!.y;
    l.actors[0].w = 10;
    const out = checkSequence(l);
    expect(out.some((s) => s.includes("labels overlap"))).toBe(true);
    expect(out.some((s) => s.includes("overflows its box"))).toBe(true);
  });

  it("says when a self message's text crosses a lifeline", () => {
    const l = layoutSequence(
      seq(actors("a", "b"), [msg("a", "a", "to itself")]),
    );
    l.messages[0].label!.x = lifeline(l, "b") - 4;
    expect(checkSequence(l).some((s) => s.includes("crosses"))).toBe(true);
  });
});
