import { describe, it, expect } from "vitest";
import ELK from "elkjs/lib/elk.bundled.js";
import type { FlowEdgeIR, FlowIR, FlowNodeIR } from "./diagramIR";
import {
  isMapping,
  layoutElk,
  layoutFlow,
  layoutMapping,
  layoutSpine,
  looksLikeCode,
  onCircle,
  onDiamond,
  tidy,
  type FlowLayout,
} from "./diagramLayout";
import { check } from "./diagramCheck";

const n = (
  id: string,
  label = id,
  shape: FlowNodeIR["shape"] = "step",
): FlowNodeIR => ({
  id,
  label,
  shape,
});
const e = (
  from: string,
  to: string,
  label = "",
  dashed = false,
): FlowEdgeIR => ({
  from,
  to,
  label,
  dashed,
  head: "end",
});
const flow = (
  nodes: FlowNodeIR[],
  edges: FlowEdgeIR[],
  extra: Partial<FlowIR> = {},
): FlowIR => ({
  kind: "flow",
  direction: "TB",
  nodes,
  edges,
  zones: [],
  ...extra,
});
const box = (l: FlowLayout, id: string) => l.nodes.find((b) => b.id === id)!;
const route = (l: FlowLayout, from: string, to: string) =>
  l.edges.find((r) => r.from === from && r.to === to)!;

// The corpus's declined-payment flow: a main path with a branch to the right
// and an edge that skips ahead along the path.
const payment = flow(
  [
    n("a", "Bestellung aufgeben"),
    n("b", "Betrag gültig?", "decision"),
    n("c", "Schwellenwert überschritten?", "decision"),
    n("d", "Zahlung ablehnen"),
    n("e", "Zahlung autorisieren"),
    n("f", "Bestellung bestätigen"),
    n("g", "Fehlermeldung anzeigen"),
  ],
  [
    e("a", "b"),
    e("b", "c", "Ja"),
    e("b", "d", "Nein"),
    e("c", "d", "Ja"),
    e("c", "e", "Nein"),
    e("e", "f"),
    e("d", "g"),
  ],
);

describe("layoutSpine", () => {
  it("runs the main path down one axis and a branch out to the right, level with its decision", () => {
    const l = layoutSpine(payment)!;
    expect(l).not.toBeNull();
    const axis = (id: string) => box(l, id).x + box(l, id).w / 2;
    for (const id of ["a", "b", "c", "d", "g"])
      expect(axis(id)).toBe(axis("a"));
    expect(box(l, "e").kind).toBe("outcome");
    expect(box(l, "e").x).toBeGreaterThan(box(l, "c").x + box(l, "c").w);
    expect(box(l, "e").y + box(l, "e").h / 2).toBe(
      box(l, "c").y + box(l, "c").h / 2,
    );
    // The chain goes on to the right, not down.
    expect(box(l, "f").x).toBeGreaterThan(box(l, "e").x);
    expect(check(l)).toEqual([]);
  });

  it("sends an edge that skips along the path through a lane on the left, with its label along the lane", () => {
    const l = layoutSpine(payment)!;
    const r = route(l, "b", "d");
    expect(r.points).toHaveLength(4);
    expect(r.points[1].x).toBeLessThan(box(l, "c").x);
    expect(r.label?.vertical).toBe(true);
    expect(r.dashed).toBe(false);
  });

  it("dashes an edge that loops back up the path", () => {
    const l = layoutSpine(
      flow(
        [n("a"), n("b"), n("c")],
        [e("a", "b"), e("b", "c"), e("c", "a", "retry")],
      ),
    )!;
    expect(route(l, "c", "a").dashed).toBe(true);
    expect(route(l, "c", "a").warn).toBe(false);
  });

  it("wraps a long decision rather than stretching the diamond to one line", () => {
    const l = layoutSpine(payment)!;
    const c = box(l, "c");
    expect(c.lines).toEqual(["Schwellenwert", "überschritten?"]);
    expect(c.w).toBeLessThanOrEqual(box(l, "b").w + 16);
  });

  it("puts each label beside its line", () => {
    const l = layoutSpine(payment)!;
    const down = route(l, "b", "c");
    expect(down.label!.x).toBeGreaterThan(down.points[0].x);
    const right = route(l, "c", "e");
    expect(right.label!.y + right.label!.h).toBeLessThan(right.points[0].y);
  });

  it("declines a shape that is not a path with branches", () => {
    // A branch rejoining the path.
    expect(
      layoutSpine(
        flow(
          [n("a"), n("b"), n("c"), n("x")],
          [e("a", "b"), e("b", "c"), e("a", "x"), e("x", "c")],
        ),
      ),
    ).toBeNull();
    // Two branches off one node.
    expect(
      layoutSpine(
        flow(
          [n("a"), n("b"), n("x"), n("y")],
          [e("a", "b"), e("a", "x"), e("a", "y")],
        ),
      ),
    ).toBeNull();
    // A second start.
    expect(
      layoutSpine(flow([n("a"), n("b"), n("z")], [e("a", "b"), e("z", "b")])),
    ).toBeNull();
    // Left to right, a zone, a self loop.
    expect(
      layoutSpine(flow([n("a"), n("b")], [e("a", "b")], { direction: "LR" })),
    ).toBeNull();
    expect(layoutSpine(flow([n("a")], [e("a", "a")]))).toBeNull();
  });
});

const mapping = flow(
  [
    { ...n("e1", "Policenantrag eingegangen"), zone: "E" },
    { ...n("e2", "Nachreichung eingegangen"), zone: "E" },
    { ...n("e3", "Vorerfassung erstellt"), zone: "E" },
    { ...n("t1", "Benachrichtigung"), zone: "T" },
    { ...n("t2", "Arbeitsunfähigkeit"), zone: "T" },
    { ...n("t3", "Vorerfassung"), zone: "T" },
    { ...n("t4", "Digitale ID"), zone: "T" },
  ],
  [
    e("e1", "t1"),
    e("e1", "t4", "ID-Prüfung nötig"),
    e("e2", "t1"),
    e("e2", "t2", "nur mit Zeiträumen"),
    e("e3", "t3"),
  ],
  {
    direction: "LR",
    zones: [
      { id: "E", title: "Ereignis" },
      { id: "T", title: "Topic" },
    ],
  },
);

describe("labels that do not fit the usual box", () => {
  it("sizes a decision with more forced lines than any wrap, instead of looping", () => {
    const l = layoutSpine(
      flow(
        [n("a"), n("d", "a\nb\nc\nd\ne\nf", "decision"), n("z")],
        [e("a", "d"), e("d", "z")],
      ),
    )!;
    expect(box(l, "d").lines).toHaveLength(6);
  });

  it("widens every box for an identifier wrapping cannot shorten", () => {
    const id = "OrderShippingCostCalculator.calculateTotal()";
    const l = layoutSpine(flow([n("a", id), n("b")], [e("a", "b")]))!;
    expect(box(l, "a").lines).toEqual([id]);
    expect(box(l, "b").w).toBe(box(l, "a").w);
    expect(check(l)).toEqual([]);
  });

  it("opens the gap for an arrow label that wrapped to several lines", () => {
    const long = "only when the cart total exceeds the free shipping limit";
    const l = layoutSpine(
      flow(
        [n("a"), n("b", "ok?", "decision"), n("c"), n("x")],
        [e("a", "b"), e("b", "c", long), e("b", "x", long)],
      ),
    )!;
    expect(route(l, "b", "c").label!.lines.length).toBeGreaterThan(1);
    expect(check(l)).toEqual([]);
  });

  it("keeps a wrapped label over a mapping entry clear of the row above", () => {
    const long = "only when the policy holds a period of incapacity";
    const ir = {
      ...mapping,
      edges: mapping.edges.map((x) =>
        x.label !== "" ? { ...x, label: long } : x,
      ),
    };
    const l = layoutMapping(ir);
    expect(l.edges.some((r) => (r.label?.lines.length ?? 0) > 1)).toBe(true);
    expect(check(l)).toEqual([]);
  });
});

describe("zones and loops", () => {
  const long = "Very very long zone title that exceeds";

  it("widens a mapping's frame for a title longer than its boxes", () => {
    const ir = {
      ...mapping,
      zones: [{ id: "E", title: long }, mapping.zones[1]],
    };
    expect(check(layoutMapping(ir))).toEqual([]);
  });

  it("widens an ELK zone for its title", async () => {
    const ir = flow([{ ...n("a"), zone: "z" }, n("b")], [e("a", "b")], {
      zones: [{ id: "z", title: long }],
    });
    expect(check(await layoutElk(ir, new ELK()))).toEqual([]);
  });

  it("dashes a loop back under ELK as the spine does", async () => {
    // A rejoining branch keeps it off the spine; c -> a runs back up.
    const ir = flow(
      [n("a"), n("b"), n("c"), n("x")],
      [e("a", "b"), e("b", "c"), e("a", "x"), e("x", "c"), e("c", "a")],
    );
    expect(layoutSpine(ir)).toBeNull();
    const l = await layoutElk(ir, new ELK());
    expect(route(l, "c", "a").dashed).toBe(true);
    expect(route(l, "a", "b").dashed).toBe(false);
  });
});

describe("layoutMapping", () => {
  it("knows a mapping from any other left-to-right flow", () => {
    expect(isMapping(mapping)).toBe(true);
    expect(isMapping({ ...mapping, direction: "TB" })).toBe(false);
    expect(
      isMapping({ ...mapping, edges: [...mapping.edges, e("t1", "e1")] }),
    ).toBe(false);
  });

  it("keeps the triggers in the order written and runs every arrow straight but the one into a shared target", () => {
    const l = layoutMapping(mapping);
    const ys = ["e1", "e2", "e3"].map((id) => box(l, id).y);
    expect([...ys].sort((a, b) => a - b)).toEqual(ys);
    const bent = l.edges
      .filter((r) => r.points.length > 2)
      .map((r) => `${r.from}->${r.to}`);
    expect(bent).toEqual(["e2->t1"]);
    expect(check(l)).toEqual([]);
  });

  it("frames each side in a zone titled as written", () => {
    const l = layoutMapping(mapping);
    expect(l.zones.map((z) => z.title)).toEqual(["Ereignis", "Topic"]);
    expect(l.zones[0].h).toBe(l.zones[1].h);
  });
});

describe("layoutElk", () => {
  it("places what the spine declines, on the same rules", async () => {
    // A branch that rejoins the path below.
    const ir = flow(
      [n("a", "check?", "decision"), n("b"), n("c"), n("x")],
      [e("a", "b", "yes"), e("b", "c"), e("a", "x", "no"), e("x", "c")],
    );
    expect(layoutSpine(ir)).toBeNull();
    const l = await layoutFlow(ir, new ELK());
    expect(l.nodes).toHaveLength(4);
    expect(l.edges.every((r) => r.points.length >= 2)).toBe(true);
    expect(check(l)).toEqual([]);
  });

  it("frames the members of a zone", async () => {
    const ir = flow([{ ...n("a"), zone: "z" }, n("b")], [e("a", "b")], {
      zones: [{ id: "z", title: "Zone" }],
    });
    const l = await layoutElk(ir, new ELK());
    const z = l.zones[0];
    const a = box(l, "a");
    expect(a.x).toBeGreaterThanOrEqual(z.x);
    expect(a.y + a.h).toBeLessThanOrEqual(z.y + z.h);
  });

  // The engine returns an edge whose ends share a zone relative to that
  // zone; drawn as given, zone b's edges landed over zone a. Decision to
  // decision, because check() re-projects a diamond's end and cannot see it.
  it("draws an edge inside a zone inside that zone, from end to end", async () => {
    const ir = flow(
      [
        { ...n("a1", "first?", "decision"), zone: "a" },
        { ...n("a2", "second?", "decision"), zone: "a" },
        { ...n("b1", "third?", "decision"), zone: "b" },
        { ...n("b2", "fourth?", "decision"), zone: "b" },
      ],
      [e("a1", "a2", "yes"), e("b1", "b2", "no")],
      {
        zones: [
          { id: "a", title: "Left" },
          { id: "b", title: "Right" },
        ],
      },
    );
    const l = await layoutElk(ir, new ELK());
    for (const r of l.edges) {
      const z = l.zones.find((z) => z.id === r.from[0])!;
      const [from, to] = [box(l, r.from), box(l, r.to)];
      const first = r.points[0];
      const last = r.points[r.points.length - 1];
      for (const p of r.points) {
        expect(p.x).toBeGreaterThanOrEqual(z.x);
        expect(p.x).toBeLessThanOrEqual(z.x + z.w);
        expect(p.y).toBeGreaterThanOrEqual(z.y);
        expect(p.y).toBeLessThanOrEqual(z.y + z.h);
      }
      expect(first.x).toBeGreaterThanOrEqual(from.x);
      expect(first.x).toBeLessThanOrEqual(from.x + from.w);
      expect(last.y).toBeLessThanOrEqual(to.y + to.h / 2);
      expect(last.y).toBeGreaterThanOrEqual(to.y);
      expect(r.label!.x).toBeGreaterThanOrEqual(z.x);
      expect(r.label!.x + r.label!.w).toBeLessThanOrEqual(z.x + z.w);
    }
  });
});

describe("looksLikeCode", () => {
  it("sets names from the code in the mono, prose in the sans", () => {
    for (const s of [
      "Answer()",
      "NothingFound",
      "parts[2]",
      "context.md + opencode.json",
      "POST /api/ask",
    ])
      expect(looksLikeCode(s), s).toBe(true);
    for (const s of [
      "Return shipment (queued)",
      "done (ok)",
      "HEAD by renovaid?",
      "Step [1]: gather",
    ])
      expect(looksLikeCode(s), s).toBe(false);
  });
});

describe("tidy and onDiamond", () => {
  it("drops repeated and collinear points", () => {
    expect(
      tidy([
        { x: 0, y: 0 },
        { x: 0, y: 0 },
        { x: 0, y: 5 },
        { x: 0, y: 10 },
        { x: 4, y: 10 },
      ]),
    ).toEqual([
      { x: 0, y: 0 },
      { x: 0, y: 10 },
      { x: 4, y: 10 },
    ]);
  });

  it("keeps the turn of a route that doubles back", () => {
    const pts = [
      { x: 100, y: 50 },
      { x: 100, y: 90 },
      { x: 100, y: 70 },
    ];
    expect(tidy(pts)).toEqual(pts);
  });

  it("moves an end from the bounding box onto the outline", () => {
    const d = {
      id: "d",
      kind: "decision" as const,
      lines: [],
      face: "sans" as const,
      size: 13,
      bold: false,
      focus: false,
      x: 0,
      y: 0,
      w: 100,
      h: 50,
    };
    expect(onDiamond(d, { x: 50, y: 0 }, { x: 50, y: -20 })).toEqual({
      x: 50,
      y: 0,
    });
    expect(onDiamond(d, { x: 75, y: 50 }, { x: 75, y: 80 })).toEqual({
      x: 75,
      y: 37.5,
    });
    expect(onDiamond(d, { x: 100, y: 25 }, { x: 130, y: 25 })).toEqual({
      x: 100,
      y: 25,
    });
  });
});

describe("a state diagram's dots and choices", () => {
  // The corpus's thread lifecycle: a start, three states, a skip ahead and a
  // loop back, a stop.
  const states = flow(
    [
      n("s", "", "start"),
      n("draft", "Draft"),
      n("shared", "Shared"),
      n("archived", "Archived"),
      n("t", "", "stop"),
    ],
    [
      e("s", "draft"),
      e("draft", "shared", "share()"),
      e("shared", "draft", "unshare()"),
      e("shared", "archived", "archive()"),
      e("draft", "archived", "archive()"),
      e("archived", "t"),
    ],
  );

  it("draws [*] as small dots on the path, without text, and meets them square", () => {
    const l = layoutSpine(states)!;
    expect(l).not.toBeNull();
    const s = box(l, "s");
    const t = box(l, "t");
    expect([s.kind, s.w, s.h, s.lines]).toEqual(["start", 16, 16, []]);
    expect([t.kind, t.w, t.h]).toEqual(["stop", 20, 20]);
    const draft = box(l, "draft");
    expect(s.x + s.w / 2).toBe(draft.x + draft.w / 2);
    expect(route(l, "shared", "draft").dashed).toBe(true);
    expect(check(l)).toEqual([]);
  });

  it("lays a dot out by ELK, an edge ending on its outline", async () => {
    const l = await layoutElk({ ...states, direction: "LR" }, new ELK());
    const s = box(l, "s");
    const end = route(l, "s", "draft").points[0];
    const d = Math.hypot(end.x - (s.x + 8), end.y - (s.y + 8));
    expect(Math.abs(d - 8)).toBeLessThanOrEqual(1.5);
    expect(check(l)).toEqual([]);
  });

  it("draws a choice with no text as a small diamond", async () => {
    const l = await layoutFlow(
      flow(
        [
          n("a", "Fetching"),
          n("c", "", "decision"),
          n("b", "Indexing"),
          n("f", "Failed"),
        ],
        [e("a", "c"), e("c", "b", "found"), e("c", "f", "missing")],
      ),
      new ELK(),
    );
    const c = box(l, "c");
    expect([c.kind, c.w, c.h]).toEqual(["decision", 28, 28]);
    expect(check(l)).toEqual([]);
  });

  it("says when an edge misses a dot", () => {
    const l = layoutSpine(states)!;
    route(l, "s", "draft").points[0].x += 30;
    route(l, "s", "draft").points[1].x += 30;
    expect(check(l).some((m) => m.includes("misses s"))).toBe(true);
  });
});

describe("onCircle", () => {
  const dot = {
    id: "d",
    kind: "start" as const,
    lines: [],
    face: "sans" as const,
    size: 13,
    bold: false,
    focus: false,
    x: 0,
    y: 0,
    w: 20,
    h: 20,
  };

  it("moves an end on a dot's box onto its circle, along its segment", () => {
    expect(onCircle(dot, { x: 10, y: 20 }, { x: 10, y: 50 })).toEqual({
      x: 10,
      y: 20,
    });
    expect(onCircle(dot, { x: 16, y: 20 }, { x: 16, y: 50 })).toEqual({
      x: 16,
      y: 18,
    });
    expect(onCircle(dot, { x: 20, y: 10 }, { x: 60, y: 10 })).toEqual({
      x: 20,
      y: 10,
    });
  });

  it("leaves an end beside the circle where it is", () => {
    expect(onCircle(dot, { x: 20, y: 0 }, { x: 20, y: -30 })).toEqual({
      x: 20,
      y: 0,
    });
  });
});
