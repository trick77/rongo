import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";

// The renderer measures text with getBBox, which jsdom lacks, so the tests
// stand in for it: parse accepts anything that does not say `bad`, and
// render answers with a marked SVG. What is under test is what diagram.tsx
// does around the renderer: source selection, the cache, the fallbacks.
const parse = vi.fn(async (src: string) => {
  if (src.includes("bad")) throw new Error("Parse error on line 1");
  return { diagramType: src.split(/\s+/)[0] };
});
const renderSvg = vi.fn(async (id: string, src: string) => ({
  svg: `<svg id="${id}" data-src="${src.length}"><g class="drawn"></g></svg>`,
}));
vi.mock("mermaid", () => ({
  default: {
    initialize: vi.fn(),
    parse: (s: string) => parse(s),
    render: (i: string, s: string) => renderSvg(i, s),
  },
}));

// rongo's own drawing stands aside by default, so the tests above it keep
// asserting on the renderer; the ones about it say what it returns.
const editorial = vi.fn(
  async (_m: unknown, _src: string, _ids: string): Promise<string | null> =>
    null,
);
vi.mock("./editorial", () => ({
  editorial: (m: unknown, s: string, i: string) => editorial(m, s, i),
}));

import Diagram, {
  diagramKind,
  diagramSource,
  diagramTitle,
  draw,
  parseDiagram,
  spread,
  themeVariables,
  type FlowSpec,
  type SequenceSpec,
} from "./diagram";

beforeEach(() => {
  parse.mockClear();
  renderSvg.mockClear();
  editorial.mockReset();
  editorial.mockResolvedValue(null);
});

describe("rongo's own drawing", () => {
  it("draws a source it covers itself, on its own parse alone", async () => {
    editorial.mockResolvedValueOnce('<svg data-own="1"></svg>');
    const out = await draw("flowchart TD\n own --> one");
    expect(out).toEqual({ svg: '<svg data-own="1"></svg>', own: true });
    expect(parse).not.toHaveBeenCalled();
    expect(renderSvg).not.toHaveBeenCalled();
  });

  it("leaves a flowchart it cannot parse to the renderer's refusal", async () => {
    editorial.mockRejectedValueOnce(new Error("Parse error"));
    const out = await draw("flowchart TD\n bad -->");
    expect(out).toEqual({ error: "Parse error on line 1" });
  });

  it("lets the renderer draw what it declines", async () => {
    const out = await draw("flowchart TD\n declined --> here");
    expect(out).toEqual({ svg: expect.stringContaining("drawn") });
    expect(renderSvg).toHaveBeenCalled();
  });

  it("never loads its drawing for a kind it does not draw", async () => {
    await draw("sequenceDiagram\n A->>B: hi");
    expect(editorial).not.toHaveBeenCalled();
  });

  it("lets the renderer draw when its own drawing throws", async () => {
    editorial.mockRejectedValueOnce(new Error("layout blew up"));
    const out = await draw("flowchart TD\n throws --> here");
    expect(out).toEqual({ svg: expect.stringContaining("drawn") });
  });

  it("sets its drawing at full size, in a frame as wide as the pane it sits in", async () => {
    editorial.mockResolvedValueOnce('<svg data-own="1" width="900"></svg>');
    const { container, findByLabelText } = render(
      <div data-diagram-pane>
        <Diagram src={"flowchart TD\n wide --> frame"} />
      </div>,
    );
    const pane = container.firstElementChild as HTMLElement;
    Object.defineProperty(pane, "clientWidth", { value: 1232 });
    await findByLabelText(/full view/);
    expect(
      container.querySelector(".rongo-diagram-natural svg"),
    ).not.toBeNull();
    // No card ground of its own: the drawing brings its canvas.
    expect(container.querySelector(".bg-panel")).toBeNull();
  });

  it("grows past the text into the pane, right first, less a gutter", async () => {
    editorial.mockResolvedValueOnce('<svg data-own="1" width="900"></svg>');
    // The layout measured in the browser: a 767px pane at x 362, the answer's
    // 600px of text at x 400.
    const pane = document.createElement("div");
    pane.setAttribute("data-diagram-pane", "");
    Object.defineProperty(pane, "clientWidth", { value: 767 });
    pane.getBoundingClientRect = () => ({ left: 362 }) as DOMRect;
    const text = document.createElement("div");
    text.getBoundingClientRect = () =>
      ({ left: 400, right: 1000, width: 600 }) as DOMRect;
    pane.appendChild(text);
    document.body.appendChild(pane);
    const { findByLabelText } = render(
      <Diagram src={"flowchart TD\n measured --> pane"} />,
      { container: text },
    );
    const button = await findByLabelText(/full view/);
    const frame = button.closest(".mt-3") as HTMLElement;
    // 113px free on the right, then the 22 on the left; the rest scrolls.
    expect(frame.style.width).toBe("735px");
    expect(frame.style.marginLeft).toBe("-22px");
    pane.remove();
  });

  it("stays in the column where there is no pane to measure", async () => {
    editorial.mockResolvedValueOnce('<svg data-own="1" width="900"></svg>');
    const { findByLabelText } = render(
      <Diagram src={"flowchart TD\n no --> pane"} />,
    );
    const button = await findByLabelText(/full view/);
    expect((button.closest(".mt-3") as HTMLElement).style.width).toBe("");
  });
});

describe("spread", () => {
  const room = { width: 600, left: 40, right: 100 };

  it("keeps a drawing that fits in the text's own width", () => {
    expect(spread(room, 600)).toBeNull();
  });

  it("grows right before it grows left", () => {
    expect(spread(room, 680)).toEqual({ marginLeft: -0, width: 680 });
    expect(spread(room, 720)).toEqual({ marginLeft: -20, width: 720 });
  });

  it("stops at the pane's gutters and leaves the rest to scroll", () => {
    expect(spread(room, 2000)).toEqual({ marginLeft: -40, width: 740 });
  });
});

describe("parseDiagram", () => {
  it("accepts a flow and a sequence, defaulting kind", () => {
    const f = parseDiagram(
      JSON.stringify({
        type: "flow",
        nodes: [{ id: "a", label: "x" }],
        edges: [],
      }),
    );
    expect(f).toEqual({
      type: "flow",
      nodes: [{ id: "a", label: "x", kind: "step" }],
      edges: [],
    });
    const s = parseDiagram(
      JSON.stringify({
        type: "sequence",
        actors: [{ id: "u", label: "UI" }],
        steps: [{ from: "u", to: "u", label: "tick" }],
      }),
    );
    expect(s?.type).toBe("sequence");
    expect((s as SequenceSpec).steps[0]).toEqual({
      from: "u",
      to: "u",
      label: "tick",
      kind: "call",
    });
  });

  it("returns null only when there is nothing to draw", () => {
    const bad = [
      "{not json",
      '"a string"',
      '{"type":"pie","nodes":[],"edges":[]}',
      '{"type":"flow","nodes":[],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":5}],"edges":[]}',
      '{"type":"sequence","actors":[],"steps":[]}',
    ];
    for (const b of bad) expect(parseDiagram(b), b).toBeNull();
  });

  it("drops the element it cannot read and draws the rest", () => {
    const unknownKind = parseDiagram(
      '{"type":"flow","nodes":[{"id":"a","label":"x","kind":"blob"}],"edges":[]}',
    ) as FlowSpec;
    expect(unknownKind.nodes[0].kind).toBe("step");

    const dupe = parseDiagram(
      '{"type":"flow","nodes":[{"id":"a","label":"x"},{"id":"a","label":"y"}],"edges":[]}',
    ) as FlowSpec;
    expect(dupe.nodes).toHaveLength(1);
    expect(dupe.nodes[0].label).toBe("x");

    const danglingEdge = parseDiagram(
      '{"type":"flow","nodes":[{"id":"a","label":"x"}],"edges":[{"from":"a","to":"zz"}]}',
    ) as FlowSpec;
    expect(danglingEdge.nodes).toHaveLength(1);
    expect(danglingEdge.edges).toHaveLength(0);

    const shout = parseDiagram(
      '{"type":"sequence","actors":[{"id":"u","label":"UI"}],"steps":[{"from":"u","to":"u","label":"x","kind":"shout"}]}',
    ) as SequenceSpec;
    expect(shout.steps[0].kind).toBe("call");

    const danglingStep = parseDiagram(
      '{"type":"sequence","actors":[{"id":"u","label":"UI"}],"steps":[{"from":"u","to":"v","label":"x"}]}',
    ) as SequenceSpec;
    expect(danglingStep.actors).toHaveLength(1);
    expect(danglingStep.steps).toHaveLength(0);
  });

  // Stored specs carry a src array per node; nothing draws or copies it any
  // more, and however it was written it never costs the node (#45).
  it("ignores a node's src, whatever shape it came in", () => {
    for (const src of ['["1"]', "[1.5]", '"1"', "[2,4]"]) {
      const body = `{"type":"flow","nodes":[{"id":"a","label":"x","src":${src}}],"edges":[]}`;
      expect((parseDiagram(body) as FlowSpec).nodes, body).toEqual([
        { id: "a", label: "x", kind: "step" },
      ]);
    }
  });
});

describe("diagramSource", () => {
  it("takes a mermaid fence as it is", () => {
    expect(diagramSource("mermaid", "flowchart LR\n a --> b")).toBe(
      "flowchart LR\n a --> b",
    );
  });

  it("converts the older JSON spec whatever the fence says", () => {
    const body = '{"type":"flow","nodes":[{"id":"a","label":"x"}],"edges":[]}';
    for (const tag of ["diagram", "json", ""]) {
      expect(diagramSource(tag, body), tag).toBe('flowchart TD\n    a["x"]');
    }
  });

  it("takes a diagram fence holding mermaid text as mermaid", () => {
    expect(diagramSource("diagram", "sequenceDiagram\n A->>B: hi")).toBe(
      "sequenceDiagram\n A->>B: hi",
    );
  });

  it("leaves code alone", () => {
    expect(diagramSource("json", '{"type":"config"}')).toBeNull();
    expect(diagramSource("go", "func main() {}")).toBeNull();
    // A spec the parser rejects is handed on, so the card can say it broke.
    expect(diagramSource("json", '{"type":"flow","nodes":[]}')).toBe(
      '{"type":"flow","nodes":[]}',
    );
  });
});

describe("diagramKind and diagramTitle", () => {
  it("reads the type off the first word, past directives and blank lines", () => {
    expect(diagramKind("flowchart LR\n a-->b")).toBe("flowchart");
    expect(diagramKind("\n%%{init: {}}%%\nsequenceDiagram\n A->>B: x")).toBe(
      "sequenceDiagram",
    );
    expect(diagramKind("stateDiagram-v2\n [*] --> A")).toBe("stateDiagram-v2");
    expect(diagramKind("")).toBe("");
    expect(diagramKind('{"type":"flow"}')).toBe("");
  });

  it("names the picture by its type and falls back to Diagram", () => {
    expect(diagramTitle("flowchart TD\n a")).toBe("Flow diagram");
    expect(diagramTitle("graph LR\n a")).toBe("Flow diagram");
    expect(diagramTitle("sequenceDiagram\n A->>B: x")).toBe("Sequence diagram");
    expect(diagramTitle("stateDiagram-v2\n a")).toBe("State diagram");
    expect(diagramTitle("erDiagram\n A ||--o{ B : has")).toBe("Entity diagram");
    expect(diagramTitle("classDiagram\n class A")).toBe("Class diagram");
    expect(diagramTitle("gantt\n title x")).toBe("Diagram");
  });
});

describe("draw", () => {
  it("renders a source once, however often it is asked for", async () => {
    const src = "flowchart LR\n once --> twice";
    const a = await draw(src);
    const b = await draw(src);
    expect(a).toBe(b);
    expect(renderSvg).toHaveBeenCalledTimes(1);
  });

  it("answers a refused source with the parser's complaint, not a throw", async () => {
    const out = await draw("flowchart LR\n bad[");
    expect(out).toEqual({ error: "Parse error on line 1" });
    expect(renderSvg).not.toHaveBeenCalled();
  });

  it("keeps a drawing but not a refusal, so a chunk that failed to load is tried again", async () => {
    await draw("flowchart LR\n bad[ again");
    await draw("flowchart LR\n bad[ again");
    expect(parse).toHaveBeenCalledTimes(2);
  });
});

describe("Diagram", () => {
  it("says a picture is on its way, then draws it", async () => {
    const { container, getByText } = render(
      <Diagram src={"sequenceDiagram\n A->>B: hi"} />,
    );
    expect(getByText("Drawing the diagram…")).toBeTruthy();
    await waitFor(() =>
      expect(container.querySelector("svg .drawn")).toBeTruthy(),
    );
    expect(
      container.querySelector('[role="img"]')?.getAttribute("aria-label"),
    ).toBe("Sequence diagram");
  });

  it("says so when the renderer refuses the source, and keeps the source", async () => {
    const { getByText, container } = render(
      <Diagram src={"flowchart LR\n bad["} />,
    );
    await waitFor(() =>
      expect(getByText("Diagram could not be drawn")).toBeTruthy(),
    );
    expect(container.querySelector("details pre code")?.textContent).toBe(
      "flowchart LR\n bad[",
    );
    expect(container.querySelector("svg")).toBeNull();
  });

  it("offers the full view and the file, without being hovered first", async () => {
    // A phone has no hover, and the full view is the only way it sees a wide
    // diagram whole.
    const { getByLabelText } = render(
      <Diagram src={"sequenceDiagram\n A->>B: x"} />,
    );
    await waitFor(() =>
      expect(getByLabelText("Sequence diagram: full view")).toBeTruthy(),
    );
    expect(getByLabelText("Sequence diagram: download SVG")).toBeTruthy();
  });

  it("opens the full view on the button and closes it again", async () => {
    const { getByLabelText, queryByRole } = render(
      <Diagram src={"sequenceDiagram\n A->>B: y"} />,
    );
    await waitFor(() =>
      expect(getByLabelText("Sequence diagram: full view")).toBeTruthy(),
    );
    expect(queryByRole("dialog")).toBeNull();
    fireEvent.click(getByLabelText("Sequence diagram: full view"));
    expect(queryByRole("dialog")).toBeTruthy();
    expect(queryByRole("dialog")?.querySelector("svg .drawn")).toBeTruthy();
    fireEvent.click(getByLabelText("Close"));
    expect(queryByRole("dialog")).toBeNull();
  });

  it("downloads the drawing under the kind of picture it is", async () => {
    let written = "";
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: () => "blob:x",
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      value: () => {},
    });
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(function (this: HTMLAnchorElement) {
        written = this.download;
      });
    const { getByLabelText } = render(
      <Diagram src={"stateDiagram-v2\n [*] --> A"} />,
    );
    await waitFor(() =>
      expect(getByLabelText("State diagram: download SVG")).toBeTruthy(),
    );
    fireEvent.click(getByLabelText("State diagram: download SVG"));
    expect(written).toBe("rongo-state-diagram.svg");
    click.mockRestore();
  });
});

describe("themeVariables", () => {
  // The tokens live in index.css, which jsdom does not load, so the test sets
  // the handful it asserts on directly. An inline custom property resolves
  // through getComputedStyle, which is the path themeVariables reads.
  const tokens: Record<string, string> = {
    "--color-panel": "#1b1b1a",
    "--color-border": "#323230",
    "--color-border-soft": "#2a2a28",
    "--color-faint": "#6f6d66",
    "--color-muted": "#9c9a92",
  };

  beforeEach(() => {
    for (const [name, value] of Object.entries(tokens)) {
      document.documentElement.style.setProperty(name, value);
    }
    return () => {
      for (const name of Object.keys(tokens)) {
        document.documentElement.style.removeProperty(name);
      }
    };
  });

  // A frame is only a frame if it reads against the fill it sits on. The border
  // token is a chrome value, where it separates a panel from the page ground;
  // against a node fill four points away from it, it draws nothing.
  it("draws every frame in a colour that reads on the fill", () => {
    const vars = themeVariables();
    for (const key of [
      "primaryBorderColor",
      "secondaryBorderColor",
      "actorBorder",
      "labelBoxBorderColor",
      "clusterBorder",
    ]) {
      expect(vars[key]).toBe(tokens["--color-faint"]);
    }
  });

  // Quieter than the arrows, so the flow stays the loudest thing drawn.
  it("keeps the frames quieter than the lines between them", () => {
    const vars = themeVariables();
    expect(vars.lineColor).toBe(tokens["--color-muted"]);
    expect(vars.primaryBorderColor).not.toBe(vars.lineColor);
  });
});
