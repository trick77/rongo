import { describe, it, expect } from "vitest";
import ELK from "elkjs/lib/elk.bundled.js";
import type { ERIR, EntityIR, RelIR } from "./diagramIR";
import { layoutER, markRoom } from "./diagramER";
import { checkER } from "./diagramCheck";
import { drawER, drawFlow } from "./diagramDraw";
import type { Palette } from "./diagramTheme";
import { layoutSpine } from "./diagramLayout";

const pal: Palette = {
  surface: "#151514",
  fg: "#ece8df",
  muted: "#a29e94",
  line: "#3b3935",
  tint: "#201f1c",
  accent: "#d97757",
  warn: "#e3a33f",
  sans: "Plex Sans",
  mono: "Plex Mono",
};

const entity = (
  id: string,
  attrs: [string, string, string[]?, string?][] = [],
  focus = false,
): EntityIR => ({
  id,
  label: id,
  attrs: attrs.map(([type, name, keys = [], comment = ""]) => ({
    type,
    name,
    keys,
    comment,
  })),
  ...(focus ? { focus: true } : {}),
});
const rel = (from: string, to: string, over: Partial<RelIR> = {}): RelIR => ({
  from,
  to,
  label: "has",
  fromCard: "one",
  toCard: "zeroOrMore",
  dashed: false,
  ...over,
});
const model = (entities: EntityIR[], rels: RelIR[]): ERIR => ({
  kind: "er",
  direction: "TB",
  entities,
  rels,
});

const threads = model(
  [
    entity("THREAD", [
      ["int", "id", ["PK"]],
      ["string", "title", [], "the name a reader gave it"],
    ]),
    entity("MESSAGE", [
      ["int", "thread_id", ["FK"]],
      ["string", "answer"],
    ]),
    entity("SHARE", [], true),
  ],
  [
    rel("THREAD", "MESSAGE", { label: "holds" }),
    rel("THREAD", "SHARE", {
      label: "is shared as",
      toCard: "zeroOrOne",
      dashed: true,
    }),
  ],
);

describe("layoutER", () => {
  it("sizes a table to its widest column, its fields in rows under its head", async () => {
    const l = await layoutER(threads, new ELK());
    const t = l.tables.get("THREAD")!;
    expect(t.rows).toHaveLength(2);
    expect(t.rows[1].y).toBeGreaterThan(t.rows[0].y);
    // Columns line up: the name of every field starts at one x.
    const names = t.rows.map(
      (r) =>
        r.cells.find((c) => c.text[0] === "id" || c.text[0] === "title")!.x,
    );
    expect(names[0]).toBe(names[1]);
    const share = l.tables.get("SHARE")!;
    expect(share.rows).toEqual([]);
    expect(checkER(l)).toEqual([]);
  });

  it("routes every relationship square, its ends long enough for their marks", async () => {
    const l = await layoutER(threads, new ELK());
    expect(l.edges).toHaveLength(2);
    expect(l.cards).toEqual([
      { from: "one", to: "zeroOrMore" },
      { from: "one", to: "zeroOrOne" },
    ]);
    expect(l.edges[1].dashed).toBe(true);
    for (const e of l.edges) expect(e.label).toBeDefined();
    expect(checkER(l)).toEqual([]);
  });

  it("sets every table's name in one face, the mono once any looks like code", async () => {
    const faces = async (...ids: string[]) =>
      [
        ...(
          await layoutER(
            model(
              ids.map((id) => entity(id)),
              [],
            ),
            new ELK(),
          )
        ).tables.values(),
      ].map((t) => t.name.face);
    expect(await faces("DorChecklist", "Verdict")).toEqual(["mono", "mono"]);
    expect(await faces("THREAD", "Verdict")).toEqual(["sans", "sans"]);
  });

  it("breaks a name and a field where the source breaks them, and makes room", async () => {
    const l = await layoutER(
      model(
        [
          {
            id: "A",
            label: "Customer\nAccount",
            attrs: [
              {
                type: "string",
                name: "line one\nline two",
                keys: [],
                comment: "",
              },
            ],
          },
        ],
        [],
      ),
      new ELK(),
    );
    const t = l.tables.get("A")!;
    expect(t.name.lines).toEqual(["Customer", "Account"]);
    expect(t.head).toBeGreaterThan(36);
    const name = t.rows[0].cells.find((c) => c.text[0] === "line one")!;
    expect(name.text).toEqual(["line one", "line two"]);
    expect(t.rows[0].h).toBeGreaterThan(22);
    const doc = parse(drawER(l, pal));
    const texts = [...doc.querySelectorAll("text")].map((x) => x.textContent);
    expect(texts).toEqual(
      expect.arrayContaining(["Customer", "Account", "line one", "line two"]),
    );
    expect(checkER(l)).toEqual([]);
  });

  it("lays a left-to-right model out left to right", async () => {
    const l = await layoutER({ ...threads, direction: "LR" }, new ELK());
    const [a, b] = ["THREAD", "MESSAGE"].map((id) =>
      l.nodes.find((n) => n.id === id)!,
    );
    expect(b.x).toBeGreaterThan(a.x + a.w);
    expect(checkER(l)).toEqual([]);
  });
});

describe("checkER", () => {
  it("says when a field overflows its table or an end is too short for its mark", async () => {
    const l = await layoutER(threads, new ELK());
    l.nodes[0].w = 60;
    const e = l.edges[0];
    const last = e.points.length - 1;
    const dx = Math.sign(e.points[last - 1].x - e.points[last].x);
    const dy = Math.sign(e.points[last - 1].y - e.points[last].y);
    e.points[last - 1] = {
      x: e.points[last].x + dx * (markRoom - 4),
      y: e.points[last].y + dy * (markRoom - 4),
    };
    const out = checkER(l);
    expect(out.some((s) => s.includes("overflows its table"))).toBe(true);
    expect(out.some((s) => s.includes("too short for its mark"))).toBe(true);
  });
});

const parse = (svg: string) =>
  new DOMParser().parseFromString(svg, "image/svg+xml");

describe("drawER", () => {
  it("writes tables, their focus in the accent, and model text as text", async () => {
    const l = await layoutER(
      model(
        [
          entity("A", [["int", '</text><script>alert("x")</script>']]),
          entity("B", [], true),
        ],
        [rel("A", "B")],
      ),
      new ELK(),
    );
    const doc = parse(drawER(l, pal));
    expect(doc.querySelector("parsererror")).toBeNull();
    expect(doc.querySelector("script")).toBeNull();
    expect(
      [...doc.querySelectorAll("text")].map((t) => t.textContent),
    ).toContain('</text><script>alert("x")</script>');
    const frames = [...doc.querySelectorAll("rect")].filter(
      (r) => r.getAttribute("fill") === "none",
    );
    expect(frames.map((r) => r.getAttribute("stroke"))).toEqual([
      pal.line,
      pal.accent,
    ]);
    expect(doc.querySelector("path")?.getAttribute("fill")).toBe(pal.tint);
  });

  it("marks each end with its cardinality: bars for one, a ring for zero, a crow's foot for many", async () => {
    const draw = async (
      fromCard: RelIR["fromCard"],
      toCard: RelIR["toCard"],
    ) => {
      const l = await layoutER(
        model(
          [entity("A"), entity("B")],
          [rel("A", "B", { fromCard, toCard, label: "" })],
        ),
        new ELK(),
      );
      const doc = parse(drawER(l, pal));
      return {
        lines: doc.querySelectorAll("line").length,
        rings: doc.querySelectorAll("circle").length,
      };
    };
    expect(await draw("one", "one")).toEqual({ lines: 4, rings: 0 });
    expect(await draw("zeroOrOne", "zeroOrMore")).toEqual({
      lines: 3,
      rings: 2,
    });
    expect(await draw("one", "oneOrMore")).toEqual({ lines: 5, rings: 0 });
  });

  it("dashes a non-identifying relationship in the muted, never the boundary amber", async () => {
    const l = await layoutER(threads, new ELK());
    const svg = drawER(l, pal);
    const lines = [...parse(svg).querySelectorAll("polyline")];
    expect(lines.map((p) => p.getAttribute("stroke-dasharray"))).toEqual([
      null,
      "5 4",
    ]);
    expect(svg).not.toContain(pal.warn);
  });
});

describe("drawFlow's dots", () => {
  it("draws a start as a filled dot and a stop as a ringed one", () => {
    const l = layoutSpine({
      kind: "flow",
      direction: "TB",
      nodes: [
        { id: "s", label: "", shape: "start" },
        { id: "a", label: "Draft", shape: "step" },
        { id: "t", label: "", shape: "stop" },
      ],
      edges: [
        { from: "s", to: "a", label: "", dashed: false, head: "end" },
        { from: "a", to: "t", label: "", dashed: false, head: "end" },
      ],
      zones: [],
    })!;
    const circles = [
      ...parse(drawFlow(l, pal, "t")).querySelectorAll("circle"),
    ];
    expect(
      circles.map((c) => [c.getAttribute("fill"), c.getAttribute("stroke")]),
    ).toEqual([
      [pal.fg, null],
      [pal.surface, pal.fg],
      [pal.fg, null],
    ]);
  });
});
