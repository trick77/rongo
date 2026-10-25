import { describe, it, expect } from "vitest";
import { check } from "./diagramCheck";
import type { EdgeRoute, FlowLayout, NodeBox } from "./diagramLayout";

const node = (
  id: string,
  x: number,
  y: number,
  over: Partial<NodeBox> = {},
): NodeBox => ({
  id,
  kind: "step",
  lines: [id],
  face: "sans",
  size: 13,
  bold: false,
  focus: false,
  x,
  y,
  w: 100,
  h: 40,
  ...over,
});
const edge = (
  from: string,
  to: string,
  pts: [number, number][],
  over: Partial<EdgeRoute> = {},
): EdgeRoute => ({
  from,
  to,
  points: pts.map(([x, y]) => ({ x, y })),
  dashed: false,
  warn: false,
  head: "end",
  ...over,
});
const layout = (nodes: NodeBox[], edges: EdgeRoute[]): FlowLayout => ({
  width: 600,
  height: 600,
  nodes,
  edges,
  zones: [],
});

const a = node("a", 0, 0);
const b = node("b", 0, 100);

describe("check", () => {
  it("passes a straight arrow between two boxes", () => {
    expect(
      check(
        layout(
          [a, b],
          [
            edge("a", "b", [
              [50, 40],
              [50, 100],
            ]),
          ],
        ),
      ),
    ).toEqual([]);
  });

  it("refuses a diagonal, and an arrow riding a border", () => {
    expect(
      check(
        layout(
          [a, b],
          [
            edge("a", "b", [
              [50, 40],
              [60, 100],
            ]),
          ],
        ),
      ),
    ).toContain("a->b: diagonal segment");
    expect(
      check(
        layout(
          [a, b],
          [
            edge("a", "b", [
              [50, 40],
              [80, 40],
              [80, 100],
            ]),
          ],
        ),
      ),
    ).toContain("a->b: leaves a along its border");
  });

  it("refuses an arrow behind a box it does not connect", () => {
    const c = node("c", 0, 200);
    const out = check(
      layout(
        [a, b, c],
        [
          edge("a", "c", [
            [50, 40],
            [50, 200],
          ]),
        ],
      ),
    );
    expect(out).toContain("a->c: passes behind b");
  });

  it("refuses a label on its line or over a box", () => {
    const tight = edge(
      "a",
      "b",
      [
        [50, 40],
        [50, 100],
      ],
      { label: { lines: ["x"], x: 52, y: 60, w: 20, h: 15 } },
    );
    expect(check(layout([a, b], [tight]))[0]).toMatch(
      /label 2px from its line/,
    );
    const over = edge(
      "a",
      "b",
      [
        [50, 40],
        [50, 100],
      ],
      { label: { lines: ["x"], x: 60, y: 30, w: 20, h: 15 } },
    );
    expect(check(layout([a, b], [over]))).toContain("a->b: label over a");
  });

  it("refuses two arrows on one stretch or one attach point, and a label too wide for its box", () => {
    const out = check(
      layout(
        [a, b],
        [
          edge("a", "b", [
            [50, 40],
            [50, 100],
          ]),
          edge(
            "a",
            "b",
            [
              [50, 40],
              [50, 100],
            ],
            { head: "none" },
          ),
        ],
      ),
    );
    expect(out).toContain("a->b and a->b: run on top of each other");
    expect(out).toContain("a->b and a->b: share an attach point");
    expect(
      check(
        layout(
          [node("w", 0, 0, { lines: ["a label far too wide for this box"] })],
          [],
        ),
      )[0],
    ).toMatch(/overflows its box/);
  });
});
