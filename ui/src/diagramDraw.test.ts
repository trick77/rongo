import { describe, it, expect } from "vitest";
import { drawFlow, shorten } from "./diagramDraw";
import type { FlowLayout, NodeBox } from "./diagramLayout";
import type { Palette } from "./diagramTheme";

const pal: Palette = {
  surface: "#192026",
  fg: "#e2e8ec",
  muted: "#98a5af",
  line: "#35414b",
  tint: "#1f2830",
  accent: "#62b4d0",
  warn: "#e3a33f",
  sans: "Plex Sans",
  mono: "Plex Mono",
};

const node = (over: Partial<NodeBox>): NodeBox => ({
  id: "a",
  kind: "step",
  lines: ["Gather"],
  face: "sans",
  size: 13,
  bold: false,
  focus: false,
  x: 24,
  y: 24,
  w: 200,
  h: 40,
  ...over,
});

const layout = (over: Partial<FlowLayout> = {}): FlowLayout => ({
  width: 400,
  height: 200,
  nodes: [node({})],
  edges: [],
  zones: [],
  ...over,
});

const parse = (svg: string) =>
  new DOMParser().parseFromString(svg, "image/svg+xml");

describe("drawFlow", () => {
  it("writes a standalone SVG at its own size on a canvas of its own", () => {
    const doc = parse(drawFlow(layout(), pal, "t"));
    const svg = doc.documentElement;
    expect(svg.getAttribute("viewBox")).toBe("0 0 400 200");
    expect(svg.getAttribute("width")).toBe("400");
    const canvas = svg.querySelector("rect");
    expect(canvas?.getAttribute("fill")).toBe(pal.surface);
    expect(doc.querySelector("parsererror")).toBeNull();
  });

  it("writes model text as text, whatever it holds", () => {
    const svg = drawFlow(
      layout({
        nodes: [node({ lines: ['</text><script>alert("x")</script>'] })],
      }),
      pal,
      "t",
    );
    const doc = parse(svg);
    expect(doc.querySelector("script")).toBeNull();
    expect(
      [...doc.querySelectorAll("text")].map((t) => t.textContent),
    ).toContain('</text><script>alert("x")</script>');
  });

  it("frames the focus in the accent at twice the weight", () => {
    const doc = parse(
      drawFlow(layout({ nodes: [node({ focus: true })] }), pal, "t"),
    );
    const frame = [...doc.querySelectorAll("rect")].at(-1)!;
    expect(frame.getAttribute("stroke")).toBe(pal.accent);
    expect(frame.getAttribute("stroke-width")).toBe("2");
  });

  it("draws an outcome in the tint, read from its left edge, and a decision as a diamond", () => {
    const doc = parse(
      drawFlow(
        layout({
          nodes: [
            node({ id: "o", kind: "outcome" }),
            node({ id: "d", kind: "decision", y: 100, h: 52 }),
          ],
        }),
        pal,
        "t",
      ),
    );
    const rects = [...doc.querySelectorAll("rect")];
    expect(rects.at(-1)!.getAttribute("fill")).toBe(pal.tint);
    expect(doc.querySelector("text")!.getAttribute("text-anchor")).toBe(
      "start",
    );
    expect(doc.querySelector("polygon")).not.toBeNull();
  });

  it("draws a dotted arrow in the boundary amber with its own head, and a loop dashed in the muted", () => {
    const doc = parse(
      drawFlow(
        layout({
          edges: [
            {
              from: "a",
              to: "b",
              points: [
                { x: 0, y: 0 },
                { x: 0, y: 40 },
              ],
              dashed: true,
              warn: true,
              head: "end",
              label: { lines: ["async"], x: 8, y: 10, w: 40, h: 15 },
            },
            {
              from: "b",
              to: "a",
              points: [
                { x: 50, y: 40 },
                { x: 50, y: 0 },
              ],
              dashed: true,
              warn: false,
              head: "end",
            },
          ],
        }),
        pal,
        "t",
      ),
    );
    const [warn, loop] = [...doc.querySelectorAll("polyline")];
    expect(warn.getAttribute("stroke")).toBe(pal.warn);
    expect(warn.getAttribute("marker-end")).toBe("url(#t-warn)");
    expect(loop.getAttribute("stroke")).toBe(pal.muted);
    expect(loop.getAttribute("stroke-dasharray")).toBe("5 4");
    const label = [...doc.querySelectorAll("text")].find(
      (t) => t.textContent === "async",
    )!;
    expect(label.getAttribute("fill")).toBe(pal.warn);
  });

  it("stands a lane label on end", () => {
    const doc = parse(
      drawFlow(
        layout({
          edges: [
            {
              from: "a",
              to: "b",
              points: [
                { x: 0, y: 0 },
                { x: 0, y: 100 },
              ],
              dashed: false,
              warn: false,
              head: "both",
              label: {
                lines: ["red"],
                x: 8,
                y: 30,
                w: 15,
                h: 40,
                vertical: true,
              },
            },
          ],
        }),
        pal,
        "t",
      ),
    );
    const t = [...doc.querySelectorAll("text")].find(
      (x) => x.textContent === "red",
    )!;
    expect(t.getAttribute("transform")).toMatch(/^rotate\(-90 /);
    expect(doc.querySelector("polyline")!.getAttribute("marker-start")).toBe(
      "url(#t-arrow)",
    );
  });
});

describe("shorten", () => {
  it("pulls an end back along its last segment, never past its start", () => {
    const pts = [
      { x: 0, y: 0 },
      { x: 0, y: 20 },
    ];
    expect(shorten(pts, false, 4).at(-1)).toEqual({ x: 0, y: 16 });
    expect(shorten(pts, true, 4)[0]).toEqual({ x: 0, y: 4 });
    expect(shorten(pts, false, 40)).toEqual(pts);
  });
});
