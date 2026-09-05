import { describe, it, expect, vi, afterEach } from "vitest";
import { toMermaid, mermaidize, toSvgFile, fileName } from "./diagramExport";
import type { FlowSpec, SequenceSpec } from "./diagram";

const seq: SequenceSpec = {
  type: "sequence",
  actors: [
    { id: "repos.yaml", label: "repos.yaml" },
    { id: "repos-yaml", label: "Startup" },
    { id: "idx", label: "Indexer" },
  ],
  steps: [
    { from: "repos.yaml", to: "repos-yaml", label: "Load repository list", kind: "call", src: [1, 2] },
    { from: "repos-yaml", to: "idx", label: "Sync specs", kind: "async", src: [] },
    { from: "idx", to: "idx", label: "Keyword indexing", kind: "call", src: [8] },
    { from: "idx", to: "repos-yaml", label: "Store files", kind: "return", src: [9] },
  ],
};

const flow: FlowSpec = {
  type: "flow",
  nodes: [
    { id: "a", label: "Answer()", kind: "start", src: [3] },
    { id: "b", label: 'sources "empty"?', kind: "decision", src: [] },
    { id: "c", label: "NothingFound", kind: "end", src: [] },
    { id: "d", label: "Stream to the model", kind: "step", src: [4] },
  ],
  edges: [
    { from: "a", to: "b" },
    { from: "b", to: "c", label: "yes [3]" },
    { from: "b", to: "d", label: "no" },
  ],
};

describe("toMermaid", () => {
  it("writes a sequence with one participant per actor", () => {
    const out = toMermaid(seq);
    expect(out.split("\n")[0]).toBe("sequenceDiagram");
    expect(out).toContain("participant idx as Indexer");
  });

  it("keeps ids apart when sanitizing collides them", () => {
    const out = toMermaid(seq);
    // "repos.yaml" and "repos-yaml" both sanitize to repos_yaml.
    expect(out).toContain("participant repos_yaml as repos.yaml");
    expect(out).toContain("participant repos_yaml_2 as Startup");
    expect(out).toContain("repos_yaml->>repos_yaml_2: Load repository list [1] [2]");
  });

  it("draws call, async and return with their own arrows", () => {
    const out = toMermaid(seq);
    expect(out).toContain("repos_yaml_2-)idx: Sync specs");
    expect(out).toContain("idx-->>repos_yaml_2: Store files [9]");
  });

  it("keeps a self-message on one actor", () => {
    expect(toMermaid(seq)).toContain("idx->>idx: Keyword indexing [8]");
  });

  it("carries the markers so they still match the sources block", () => {
    expect(toMermaid(seq)).toContain("[1] [2]");
  });

  it("renames an id mermaid reads as syntax", () => {
    // "end" is the flow spec's own node kind, so it is what a model writes.
    // Left alone it closes a block in a sequence and fails to parse in a
    // flowchart, taking the picture with it.
    const out = toMermaid({
      type: "flow",
      nodes: [
        { id: "start", label: "Begin", kind: "start", src: [] },
        { id: "end", label: "Done", kind: "end", src: [] },
      ],
      edges: [{ from: "start", to: "end" }],
    });
    expect(out).toContain('end_(["Done"])');
    expect(out).toContain("start --> end_");
    expect(out).not.toMatch(/^\s+end\(/m);
  });

  it("falls back to the id when an actor has no label", () => {
    const out = toMermaid({
      type: "sequence",
      actors: [{ id: "ui", label: "" }],
      steps: [{ from: "ui", to: "ui", label: "tick", kind: "call", src: [] }],
    });
    expect(out).toContain("participant ui as ui");
    expect(out).not.toMatch(/ as\s*$/m);
  });

  it("writes a flow with the kind in the node shape", () => {
    const out = toMermaid(flow);
    expect(out.split("\n")[0]).toBe("flowchart TD");
    expect(out).toContain('a(["Answer() [3]"])');
    expect(out).toContain('d["Stream to the model [4]"]');
    expect(out).toContain("{");
  });

  it("escapes a quote inside a label rather than ending it early", () => {
    expect(toMermaid(flow)).toContain("#quot;empty#quot;");
  });

  it("quotes an edge label so a marker survives the parser", () => {
    expect(toMermaid(flow)).toContain('a --> b');
    expect(toMermaid(flow)).toContain('b -->|"yes [3]"| c');
  });
});

describe("mermaidize", () => {
  const body = JSON.stringify({
    type: "sequence",
    actors: [{ id: "ui", label: "Ask.tsx" }],
    steps: [{ from: "ui", to: "ui", label: "render", kind: "call", src: [1] }],
  });

  it("replaces a diagram fence with a mermaid one", () => {
    const out = mermaidize(`Before [1].\n\n\`\`\`diagram\n${body}\n\`\`\`\n\nAfter.`);
    expect(out).toContain("```mermaid");
    expect(out).toContain("sequenceDiagram");
    expect(out).not.toContain("```diagram");
    expect(out).not.toContain('"actors"');
    expect(out).toContain("Before [1].");
    expect(out).toContain("After.");
  });

  it("leaves prose and other fences alone", () => {
    const src = "Text.\n\n```go\nfunc main() {}\n```\n";
    expect(mermaidize(src)).toBe(src);
  });

  it("leaves a diagram fence that does not parse exactly as it is", () => {
    const src = "```diagram\n{not json\n```\n";
    expect(mermaidize(src)).toBe(src);
  });

  it("leaves an unclosed diagram fence as the text it is", () => {
    const src = "```diagram\n{half";
    expect(mermaidize(src)).toBe(src);
  });
});

describe("toSvgFile", () => {
  afterEach(() => {
    document.head.querySelectorAll("style[data-test]").forEach((s) => s.remove());
  });

  function drawn(): SVGSVGElement {
    const style = document.createElement("style");
    style.setAttribute("data-test", "1");
    style.textContent =
      ".fill-active { fill: #2c2c2a; } .stroke-border { stroke: #323230; } .card { background-color: #1b1b1a; }";
    document.head.appendChild(style);
    const host = document.createElement("div");
    host.className = "card";
    host.innerHTML =
      '<svg xmlns="http://www.w3.org/2000/svg" width="240" height="90" role="img" aria-label="Sequence diagram">' +
      "<title>Sequence diagram</title>" +
      '<rect class="fill-active stroke-border" x="0" y="0" width="10" height="10"></rect>' +
      '<rect data-export="skip" x="0" y="0" width="30" height="30"></rect>' +
      "</svg>";
    document.body.appendChild(host);
    return host.querySelector("svg") as SVGSVGElement;
  }

  it("inlines the colours the classes carried", () => {
    const out = toSvgFile(drawn());
    expect(out).toContain('fill="rgb(44, 44, 42)"');
    expect(out).toContain('stroke="rgb(50, 50, 48)"');
  });

  it("leaves the elements that carry no paint unpainted", () => {
    const out = toSvgFile(drawn());
    expect(out).toMatch(/<svg[^>]*>/);
    expect(out).not.toMatch(/<svg[^>]*fill=/);
  });

  it("leaves no class behind for a viewer that has no stylesheet", () => {
    expect(toSvgFile(drawn())).not.toContain("class=");
  });

  it("drops the chips' hit rects, which are invisible in a file", () => {
    expect(toSvgFile(drawn())).not.toContain('width="30"');
  });

  it("carries the card's ground, so the pale labels are not lost on white", () => {
    const out = toSvgFile(drawn());
    expect(out).toMatch(/<rect width="240" height="90" fill="rgb\(27, 27, 26\)"\/?>/);
    // First, so it sits behind the drawing rather than over it.
    expect(out.indexOf('fill="rgb(27, 27, 26)"')).toBeLessThan(out.indexOf('width="10"'));
  });

  it("adds a viewBox so the file scales in a viewer", () => {
    const out = toSvgFile(drawn());
    expect(out).toContain('viewBox="0 0 240 90"');
    expect(out).toContain("http://www.w3.org/2000/svg");
    expect(out).toContain("<title>Sequence diagram</title>");
  });

  it("keeps the drawing out of the live tree", () => {
    const el = drawn();
    toSvgFile(el);
    expect(el.querySelectorAll('[data-export="skip"]').length).toBe(1);
  });
});

describe("fileName", () => {
  it("names the file after the kind of picture", () => {
    expect(fileName(seq)).toBe("rongo-sequence-diagram.svg");
    expect(fileName(flow)).toBe("rongo-flow-diagram.svg");
  });
});

describe("download", () => {
  it("hands the browser a blob and lets it go again", async () => {
    const { download } = await import("./diagramExport");
    const create = vi.fn(() => "blob:x");
    const revoke = vi.fn();
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    Object.defineProperty(URL, "createObjectURL", { value: create, configurable: true });
    Object.defineProperty(URL, "revokeObjectURL", { value: revoke, configurable: true });
    download("d.svg", "<svg/>");
    expect(create).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    expect(revoke).toHaveBeenCalledWith("blob:x");
    click.mockRestore();
  });
});
