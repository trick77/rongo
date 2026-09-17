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
  default: { initialize: vi.fn(), parse: (s: string) => parse(s), render: (i: string, s: string) => renderSvg(i, s) },
}));

import Diagram, {
  diagramKind,
  diagramSource,
  diagramTitle,
  draw,
  parseDiagram,
  type FlowSpec,
  type SequenceSpec,
} from "./diagram";

beforeEach(() => {
  parse.mockClear();
  renderSvg.mockClear();
});

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

  // A src array the backend never renumbered still carries the prompt's own
  // indices. Renumbering misses the whole array on one bad entry, so keeping
  // the well-formed ones would draw chips naming the wrong files.
  it("drops a src array renumbering would have missed, whole", () => {
    for (const body of [
      '{"type":"flow","nodes":[{"id":"a","label":"x","src":["1"]}],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":"x","src":[0]}],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":"x","src":[1.5]}],"edges":[]}',
      '{"type":"flow","nodes":[{"id":"a","label":"x","src":"1"}],"edges":[]}',
    ]) {
      expect((parseDiagram(body) as FlowSpec).nodes[0].src, body).toEqual([]);
    }
    // The backend leaves this array exactly as it came, so 2 and 4 are still
    // the prompt's numbers: drawn as chips they would name the wrong files.
    const mixed = parseDiagram(
      '{"type":"flow","nodes":[{"id":"a","label":"x","src":[2,"3",4]}],"edges":[]}',
    ) as FlowSpec;
    expect(mixed.nodes[0].src).toEqual([]);
  });
});


describe("diagramSource", () => {
  it("takes a mermaid fence as it is", () => {
    expect(diagramSource("mermaid", "flowchart LR\n a --> b")).toBe("flowchart LR\n a --> b");
  });

  it("converts the older JSON spec whatever the fence says", () => {
    const body = '{"type":"flow","nodes":[{"id":"a","label":"x"}],"edges":[]}';
    for (const tag of ["diagram", "json", ""]) {
      expect(diagramSource(tag, body), tag).toBe('flowchart TD\n    a["x"]');
    }
  });

  it("takes a diagram fence holding mermaid text as mermaid", () => {
    expect(diagramSource("diagram", "sequenceDiagram\n A->>B: hi")).toBe("sequenceDiagram\n A->>B: hi");
  });

  it("leaves code alone", () => {
    expect(diagramSource("json", '{"type":"config"}')).toBeNull();
    expect(diagramSource("go", "func main() {}")).toBeNull();
    // A spec the parser rejects is handed on, so the card can say it broke.
    expect(diagramSource("json", '{"type":"flow","nodes":[]}')).toBe('{"type":"flow","nodes":[]}');
  });
});

describe("diagramKind and diagramTitle", () => {
  it("reads the type off the first word, past directives and blank lines", () => {
    expect(diagramKind("flowchart LR\n a-->b")).toBe("flowchart");
    expect(diagramKind("\n%%{init: {}}%%\nsequenceDiagram\n A->>B: x")).toBe("sequenceDiagram");
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
    const { container, getByText } = render(<Diagram src={"sequenceDiagram\n A->>B: hi"} />);
    expect(getByText("Drawing the diagram…")).toBeTruthy();
    await waitFor(() => expect(container.querySelector("svg .drawn")).toBeTruthy());
    expect(container.querySelector('[role="img"]')?.getAttribute("aria-label")).toBe("Sequence diagram");
  });

  it("says so when the renderer refuses the source, and keeps the source", async () => {
    const { getByText, container } = render(<Diagram src={"flowchart LR\n bad["} />);
    await waitFor(() => expect(getByText("Diagram could not be drawn")).toBeTruthy());
    expect(container.querySelector("details pre code")?.textContent).toBe("flowchart LR\n bad[");
    expect(container.querySelector("svg")).toBeNull();
  });

  it("offers the full view and the file, without being hovered first", async () => {
    // A phone has no hover, and the full view is the only way it sees a wide
    // diagram whole.
    const { getByLabelText } = render(<Diagram src={"sequenceDiagram\n A->>B: x"} />);
    await waitFor(() => expect(getByLabelText("Sequence diagram: full view")).toBeTruthy());
    expect(getByLabelText("Sequence diagram: download SVG")).toBeTruthy();
  });

  it("opens the full view on the button and closes it again", async () => {
    const { getByLabelText, queryByRole } = render(<Diagram src={"sequenceDiagram\n A->>B: y"} />);
    await waitFor(() => expect(getByLabelText("Sequence diagram: full view")).toBeTruthy());
    expect(queryByRole("dialog")).toBeNull();
    fireEvent.click(getByLabelText("Sequence diagram: full view"));
    expect(queryByRole("dialog")).toBeTruthy();
    expect(queryByRole("dialog")?.querySelector("svg .drawn")).toBeTruthy();
    fireEvent.click(getByLabelText("Close"));
    expect(queryByRole("dialog")).toBeNull();
  });

  it("downloads the drawing under the kind of picture it is", async () => {
    let written = "";
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: () => "blob:x" });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: () => {} });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      written = this.download;
    });
    const { getByLabelText } = render(<Diagram src={"stateDiagram-v2\n [*] --> A"} />);
    await waitFor(() => expect(getByLabelText("State diagram: download SVG")).toBeTruthy());
    fireEvent.click(getByLabelText("State diagram: download SVG"));
    expect(written).toBe("rongo-state-diagram.svg");
    click.mockRestore();
  });
});
