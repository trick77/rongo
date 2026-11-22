import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  render,
  fireEvent,
  createEvent,
  waitFor,
} from "@testing-library/react";

// The drawing is the package's (@trick77/diagram, tested there): what is
// under test here is rongo's side of it, the legacy JSON fence, the card's
// wiring and the full view in rongo's overlay. draw stands in for the
// package's: it answers with an own drawing, a renderer drawing or a
// refusal, by the source.
const draw = vi.fn(async (src: string) => {
  if (src.includes("bad")) return { error: "Parse error on line 1" };
  if (src.startsWith("classDiagram"))
    return { svg: '<svg data-by="renderer"><g class="drawn"></g></svg>' };
  return {
    svg: '<svg viewBox="0 0 300 120" width="300"><g class="drawn"></g></svg>',
    own: true as const,
  };
});
// Hoisted: diagram.tsx calls it as it is imported.
const { configure } = vi.hoisted(() => ({ configure: vi.fn() }));
vi.mock("@trick77/diagram/mermaid", async (original) => ({
  ...(await original<typeof import("@trick77/diagram/mermaid")>()),
  draw: (src: string) => draw(src),
  configure: (o: unknown) => configure(o),
}));

import Diagram, {
  diagramSource,
  parseDiagram,
  type FlowSpec,
  type SequenceSpec,
} from "./diagram";

// Read at import, before beforeEach clears the mocks.
const configured = configure.mock.calls.slice();

beforeEach(() => {
  draw.mockClear();
});

describe("the package's drawing", () => {
  it("names every drawing rongo's, so its ids stay apart from the page's", () => {
    expect(configured).toEqual([[{ ids: "rongo-diagram" }]]);
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

  it("keeps the buttons above its own drawing, on no ground of its own", async () => {
    const { container, findByLabelText } = render(
      <Diagram src={"flowchart TD\n tools --> above"} />,
    );
    const row = (await findByLabelText(/full view/)).parentElement!;
    expect(row.className).not.toContain("absolute");
    expect(container.querySelector(".bg-panel")).toBeNull();
  });

  it("frames the renderer's drawing, its buttons over the frame's corner", async () => {
    const { container, findByLabelText } = render(
      <Diagram src={"classDiagram\n class A"} />,
    );
    const row = (await findByLabelText("Class diagram: full view"))
      .parentElement!;
    expect(row.className).toContain("absolute");
    expect(container.querySelector(".bg-panel svg")).not.toBeNull();
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
    const saved = saving();
    const { getByLabelText } = render(
      <Diagram src={"stateDiagram-v2\n [*] --> A"} />,
    );
    await waitFor(() =>
      expect(getByLabelText("State diagram: download SVG")).toBeTruthy(),
    );
    fireEvent.click(getByLabelText("State diagram: download SVG"));
    expect(saved.name).toBe("rongo-state-diagram.svg");
  });
});

// Read before any test stubs them.
const originalCreateObjectURL = URL.createObjectURL;
const originalRevokeObjectURL = URL.revokeObjectURL;

/** What saving() replaced, put back after every test whatever it asserted:
 * a failed expectation must not leave the next test a stubbed URL or a
 * mocked click. */
const restores: (() => void)[] = [];
afterEach(async () => {
  // The package revokes the URL on a timer after the click: let it run
  // against the stub before the stub goes.
  await new Promise((resolve) => setTimeout(resolve, 0));
  while (restores.length > 0) restores.pop()!();
});

/** saving catches a download's file name. */
function saving(): { name: string } {
  const out = { name: "" };
  for (const key of ["createObjectURL", "revokeObjectURL"] as const) {
    const was = Object.getOwnPropertyDescriptor(URL, key);
    Object.defineProperty(URL, key, {
      configurable: true,
      value: key === "createObjectURL" ? () => "blob:x" : () => {},
    });
    restores.push(() => {
      if (was) Object.defineProperty(URL, key, was);
      else delete (URL as unknown as Record<string, unknown>)[key];
    });
  }
  const click = vi
    .spyOn(HTMLAnchorElement.prototype, "click")
    .mockImplementation(function (this: HTMLAnchorElement) {
      out.name = this.download;
    });
  restores.push(() => click.mockRestore());
  return out;
}

describe("saving", () => {
  it("leaves no stub behind for the next test", () => {
    // Runs after the download tests above: their stubs are gone.
    expect(vi.isMockFunction(HTMLAnchorElement.prototype.click)).toBe(false);
    expect(URL.createObjectURL).toBe(originalCreateObjectURL);
    expect(URL.revokeObjectURL).toBe(originalRevokeObjectURL);
  });
});

/** The full view as rongo opens it: the package's sheet in rongo's overlay,
 * whose keyboard and scrim are rongo's to keep. */
describe("the full view", () => {
  async function view() {
    const r = render(
      <Diagram
        src={
          "sequenceDiagram\n  Ask->>httpapi: POST /api/ask\n  httpapi-->>Ask: stream"
        }
      />,
    );
    fireEvent.click(await r.findByLabelText("Sequence diagram: full view"));
    return r;
  }

  it("opens with the focus on its close button", async () => {
    const { getByLabelText } = await view();
    expect(document.activeElement).toBe(getByLabelText("Close"));
  });

  it("names the picture in its header", async () => {
    const { getByRole } = await view();
    expect(getByRole("dialog").getAttribute("aria-label")).toBe(
      "Sequence diagram",
    );
    expect(getByRole("dialog").querySelector("svg .drawn")).toBeTruthy();
  });

  it("closes on Escape", async () => {
    const { queryByRole } = await view();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(queryByRole("dialog")).toBeNull();
  });

  it("closes when the scrim itself is clicked, not the sheet", async () => {
    // The click, not the press: the sheet is gone by the time the click is
    // dispatched, and it would otherwise land on the answer behind it.
    const { getByRole, queryByRole } = await view();
    const dialog = getByRole("dialog");
    const scrim = dialog.parentElement as HTMLElement;

    fireEvent(dialog, createEvent.pointerDown(dialog, { bubbles: true }));
    fireEvent.click(dialog);
    expect(queryByRole("dialog")).not.toBeNull();

    fireEvent(scrim, createEvent.pointerDown(scrim, { bubbles: true }));
    expect(queryByRole("dialog")).not.toBeNull();
    fireEvent.click(scrim);
    expect(queryByRole("dialog")).toBeNull();
  });

  it("takes a drag across the edge for a drag, not a cancel", async () => {
    const { getByRole, queryByRole } = await view();
    const dialog = getByRole("dialog");
    const scrim = dialog.parentElement as HTMLElement;

    fireEvent(dialog, createEvent.pointerDown(dialog, { bubbles: true }));
    fireEvent(scrim, createEvent.pointerUp(scrim, { bubbles: true }));
    fireEvent.click(scrim);
    expect(queryByRole("dialog")).not.toBeNull();

    fireEvent(scrim, createEvent.pointerDown(scrim, { bubbles: true }));
    fireEvent(dialog, createEvent.pointerUp(dialog, { bubbles: true }));
    fireEvent.click(scrim);
    expect(queryByRole("dialog")).not.toBeNull();
  });

  it("keeps Tab inside, so it cannot reach the chips behind the scrim", async () => {
    // Those chips are focusable groups; Enter on one would open SourceView
    // under this dialog, two z-30 overlays deep.
    const { getByRole, getByLabelText, getByText } = await view();
    const dialog = getByRole("dialog");
    const close = getByLabelText("Close");
    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(document, { key: "Tab" });
    expect(dialog.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).toBe(getByText("Download SVG"));
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("draws its chrome in the sans face, not the answer's serif prose", async () => {
    const { getByRole } = await view();
    expect(getByRole("dialog").className).toContain("font-sans");
  });

  it("downloads the picture it is showing", async () => {
    const saved = saving();
    const { getByText } = await view();
    fireEvent.click(getByText("Download SVG"));
    expect(saved.name).toBe("rongo-sequence-diagram.svg");
  });
});
