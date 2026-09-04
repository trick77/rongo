import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import Diagram, { parseDiagram, wrap, layoutFlow, layoutSequence, NODE_W, type FlowSpec, type SequenceSpec } from "./diagram";

const flow: FlowSpec = {
  type: "flow",
  nodes: [
    { id: "a", label: "Answer()", kind: "start", src: [3] },
    { id: "b", label: "sources empty?", kind: "decision", src: [3] },
    { id: "c", label: "NothingFound", kind: "end", src: [] },
    { id: "d", label: "Stream to the model", kind: "step", src: [3, 4] },
    { id: "e", label: "Answer stored", kind: "end", src: [6] },
  ],
  edges: [
    { from: "a", to: "b" },
    { from: "b", to: "c", label: "yes" },
    { from: "b", to: "d", label: "no" },
    { from: "d", to: "e" },
  ],
};

const seq: SequenceSpec = {
  type: "sequence",
  actors: [
    { id: "ui", label: "Ask.tsx" },
    { id: "api", label: "httpapi" },
  ],
  steps: [
    { from: "ui", to: "api", label: "POST /api/ask", kind: "call", src: [1] },
    { from: "api", to: "ui", label: "token events", kind: "return", src: [2] },
    { from: "api", to: "api", label: "flush", kind: "async", src: [] },
  ],
};

describe("parseDiagram", () => {
  it("accepts a flow and a sequence, defaulting kind and src", () => {
    const f = parseDiagram(JSON.stringify({ type: "flow", nodes: [{ id: "a", label: "x" }], edges: [] }));
    expect(f).toEqual({ type: "flow", nodes: [{ id: "a", label: "x", kind: "step", src: [] }], edges: [] });
    const s = parseDiagram(
      JSON.stringify({ type: "sequence", actors: [{ id: "u", label: "UI" }], steps: [{ from: "u", to: "u", label: "tick" }] }),
    );
    expect(s?.type).toBe("sequence");
    expect((s as SequenceSpec).steps[0]).toEqual({ from: "u", to: "u", label: "tick", kind: "call", src: [] });
  });

  it("rejects what it cannot draw, so the block stays text", () => {
    const bad = [
      "{not json",
      '"a string"',
      '{"type":"pie","nodes":[],"edges":[]}',
      '{"type":"flow","nodes":[],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":"x","kind":"blob"}],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":"x"},{"id":"a","label":"y"}],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":"x"}],"edges":[{"from":"a","to":"zz"}]}',
      '{"type":"flow","nodes":[{"id":"a","label":"x","src":["1"]}],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":"x","src":[0]}],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":5}],"edges":[]}',
      '{"type":"sequence","actors":[{"id":"u","label":"UI"}],"steps":[{"from":"u","to":"v","label":"x"}]}',
      '{"type":"sequence","actors":[{"id":"u","label":"UI"}],"steps":[{"from":"u","to":"u","label":"x","kind":"shout"}]}',
      JSON.stringify({ type: "flow", nodes: Array.from({ length: 13 }, (_, i) => ({ id: `n${i}`, label: "x" })), edges: [] }),
      JSON.stringify({ type: "sequence", actors: Array.from({ length: 6 }, (_, i) => ({ id: `a${i}`, label: "x" })), steps: [] }),
      JSON.stringify({
        type: "sequence",
        actors: [{ id: "u", label: "UI" }],
        steps: Array.from({ length: 13 }, () => ({ from: "u", to: "u", label: "x" })),
      }),
    ];
    for (const b of bad) expect(parseDiagram(b), b).toBeNull();
  });
});

describe("wrap", () => {
  it("breaks on words at the column and caps at three lines with an ellipsis", () => {
    expect(wrap("citationsFor drops unbacked markers")).toEqual(["citationsFor drops", "unbacked markers"]);
    expect(wrap("one two three four five six seven eight nine ten eleven twelve thirteen fourteen")).toHaveLength(3);
    expect(wrap("one two three four five six seven eight nine ten eleven twelve thirteen fourteen")[2]).toMatch(/…$/);
    expect(wrap("averyveryverylongidentifierwithoutspaces")).toEqual(["averyveryverylongid…"]);
  });
});

describe("layoutFlow", () => {
  it("ranks a chain top-down on one column", () => {
    const l = layoutFlow({
      type: "flow",
      nodes: [
        { id: "a", label: "a", kind: "start", src: [] },
        { id: "b", label: "b", kind: "step", src: [] },
        { id: "c", label: "c", kind: "end", src: [] },
      ],
      edges: [
        { from: "a", to: "b" },
        { from: "b", to: "c" },
      ],
    });
    expect(l.ranks).toEqual([["a"], ["b"], ["c"]]);
    expect(l.nodes.map((n) => n.x)).toEqual([0, 0, 0]);
    expect(l.nodes[0].y).toBeLessThan(l.nodes[1].y);
    expect(l.nodes[1].y).toBeLessThan(l.nodes[2].y);
    expect(l.width).toBe(NODE_W);
    expect(l.edges.every((e) => !e.back)).toBe(true);
  });

  it("puts the branches of a decision side by side and labels the exits", () => {
    const l = layoutFlow(flow);
    expect(l.ranks).toEqual([["a"], ["b"], ["c", "d"], ["e"]]);
    const c = l.nodes.find((n) => n.id === "c")!;
    const d = l.nodes.find((n) => n.id === "d")!;
    expect(c.y).toBe(d.y);
    expect(c.x).toBeLessThan(d.x);
    expect(l.edges.find((e) => e.key.startsWith("b->c"))?.label).toBe("yes");
    // The decision leaves by its side points, not its bottom: the "yes" exit
    // starts on b's left edge, level with its middle.
    const b = l.nodes.find((n) => n.id === "b")!;
    expect(l.edges.find((e) => e.key.startsWith("b->c"))?.d).toBe(
      `M${b.x} ${b.y + b.h / 2} H${c.x + c.w / 2} V${c.y - 2}`,
    );
    expect(l.width).toBe(2 * NODE_W + 32);
  });

  it("orders a rank by its parents so branches do not cross", () => {
    const l = layoutFlow({
      type: "flow",
      nodes: [
        { id: "l", label: "l", kind: "step", src: [] },
        { id: "r", label: "r", kind: "step", src: [] },
        { id: "rr", label: "rr", kind: "step", src: [] },
        { id: "ll", label: "ll", kind: "step", src: [] },
      ],
      edges: [
        { from: "r", to: "rr" },
        { from: "l", to: "ll" },
      ],
    });
    expect(l.ranks).toEqual([
      ["l", "r"],
      ["ll", "rr"],
    ]);
  });

  it("routes a cycle's back edge up a lane right of the column", () => {
    const l = layoutFlow({
      type: "flow",
      nodes: [
        { id: "a", label: "a", kind: "step", src: [] },
        { id: "b", label: "b", kind: "step", src: [] },
      ],
      edges: [
        { from: "a", to: "b" },
        { from: "b", to: "a", label: "retry" },
      ],
    });
    expect(l.ranks).toEqual([["a"], ["b"]]);
    const back = l.edges.find((e) => e.back)!;
    expect(back.label).toBe("retry");
    expect(back.lx).toBeGreaterThan(NODE_W);
    expect(l.width).toBeGreaterThan(NODE_W);
  });

  it("routes an edge that skips a rank through the lane too", () => {
    const l = layoutFlow({
      type: "flow",
      nodes: [
        { id: "a", label: "a", kind: "step", src: [] },
        { id: "b", label: "b", kind: "step", src: [] },
        { id: "c", label: "c", kind: "step", src: [] },
      ],
      edges: [
        { from: "a", to: "b" },
        { from: "b", to: "c" },
        { from: "a", to: "c" },
      ],
    });
    const skip = l.edges.find((e) => e.key.startsWith("a->c"))!;
    expect(skip.back).toBe(false);
    expect(skip.lx).toBeGreaterThan(NODE_W);
  });

  it("is deterministic: the same spec lays out to the same coordinates", () => {
    const a = layoutFlow(flow);
    const b = layoutFlow(flow);
    expect(a).toEqual(b);
    expect(a.nodes.map((n) => [n.id, n.x, n.y, n.h])).toEqual([
      ["a", 91, 0, 40],
      ["b", 91, 88, 56],
      ["c", 0, 192, 40],
      ["d", 182, 192, 40],
      ["e", 91, 280, 40],
    ]);
  });
});

describe("layoutSequence", () => {
  it("spaces the lifelines by actor index and stacks the steps", () => {
    const l = layoutSequence(seq);
    expect(l.actors.map((a) => a.x)).toEqual([55, 195]);
    expect(l.steps[0].y).toBeLessThan(l.steps[1].y);
    expect(l.steps[1].y).toBeLessThan(l.steps[2].y);
    expect(l.steps[2].self).toBe(true);
    expect(l.width).toBe(250);
    // A return runs right to left; a self message loops out and back.
    expect(l.steps[1].x1).toBeGreaterThan(l.steps[1].x2);
    expect(l.steps[2].d).toMatch(/^M195 \d+ H225 V/);
  });
});

describe("Diagram", () => {
  it("draws a flow as SVG shapes: pills for start and end, a diamond for a decision", () => {
    const { container } = render(<Diagram spec={flow} hooks={{}} />);
    const svg = container.querySelector("svg")!;
    expect(svg.getAttribute("role")).toBe("img");
    expect(container.querySelectorAll("polygon").length).toBe(1);
    const rects = Array.from(container.querySelectorAll("rect")).filter((r) => r.getAttribute("height") === "40");
    expect(rects.some((r) => r.getAttribute("rx") === "20")).toBe(true);
    expect(rects.some((r) => r.getAttribute("rx") === "8")).toBe(true);
    expect(container.textContent).toContain("sources empty?");
    expect(container.textContent).toContain("yes");
  });

  it("draws a sequence with dashed returns and an open head for async", () => {
    const { container } = render(<Diagram spec={seq} hooks={{}} />);
    const paths = Array.from(container.querySelectorAll("g > path[marker-end]"));
    expect(paths.length).toBe(3);
    expect(paths[1].getAttribute("stroke-dasharray")).toBe("4 3");
    expect(paths[2].getAttribute("marker-end")).toMatch(/open\)$/);
    expect(paths[0].getAttribute("marker-end")).toMatch(/head\)$/);
    expect(container.querySelectorAll("line").length).toBe(2);
  });

  describe("marker chips", () => {
    it("wear the chip look while the citations are unknown, one per source", () => {
      const { container } = render(<Diagram spec={flow} hooks={{}} />);
      expect(container.textContent).toContain("[3]");
      expect(container.textContent).toContain("[4]");
      // Five src entries across the five nodes: [3] [3] [] [3,4] [6].
      const chips = container.querySelectorAll("rect.fill-accent-dim");
      expect(chips.length).toBe(5);
    });

    it("drop a number no source backs to plain text with no hover", () => {
      const onHover = vi.fn();
      const { container } = render(<Diagram spec={flow} hooks={{ backed: new Set([3, 4]), onHover }} />);
      const plain = Array.from(container.querySelectorAll("text.fill-muted")).find((t) => t.textContent === "[6]")!;
      expect(plain).toBeTruthy();
      fireEvent.mouseEnter(plain);
      expect(onHover).not.toHaveBeenCalled();
      // The four backed entries keep their chips; only [6] drops out.
      expect(container.querySelectorAll("rect.fill-accent-dim").length).toBe(4);
    });

    it("hand a hovered chip to the Sources pane, and nothing while unknown", () => {
      const onHover = vi.fn();
      const { container } = render(<Diagram spec={flow} hooks={{ backed: new Set([3, 4, 6]), onHover }} />);
      const chip = container.querySelector("rect.fill-accent-dim")!.parentElement!;
      fireEvent.mouseEnter(chip);
      expect(onHover).toHaveBeenLastCalledWith(3);
      fireEvent.mouseLeave(chip);
      expect(onHover).toHaveBeenLastCalledWith(null);

      const unknown = vi.fn();
      const r2 = render(<Diagram spec={flow} hooks={{ onHover: unknown }} />);
      fireEvent.mouseEnter(r2.container.querySelector("rect.fill-accent-dim")!.parentElement!);
      expect(unknown).not.toHaveBeenCalled();
    });
  });

  it("gives two diagrams on one page distinct arrowhead ids", () => {
    const { container } = render(
      <>
        <Diagram spec={flow} hooks={{}} />
        <Diagram spec={seq} hooks={{}} />
      </>,
    );
    const ids = Array.from(container.querySelectorAll("marker")).map((m) => m.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids.every((id) => /^[A-Za-z0-9_-]+$/.test(id))).toBe(true);
  });
});
