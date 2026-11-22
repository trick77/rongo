import { describe, it, expect } from "vitest";
import { toMermaid, mermaidize } from "./diagramExport";
import type { FlowSpec, SequenceSpec } from "./diagram";

const seq: SequenceSpec = {
  type: "sequence",
  actors: [
    { id: "repos.yaml", label: "repos.yaml" },
    { id: "repos-yaml", label: "Startup" },
    { id: "idx", label: "Indexer" },
  ],
  steps: [
    {
      from: "repos.yaml",
      to: "repos-yaml",
      label: "Load repository list",
      kind: "call",
    },
    {
      from: "repos-yaml",
      to: "idx",
      label: "Sync specs",
      kind: "async",
    },
    {
      from: "idx",
      to: "idx",
      label: "Keyword indexing",
      kind: "call",
    },
    {
      from: "idx",
      to: "repos-yaml",
      label: "Store files",
      kind: "return",
    },
  ],
};

const flow: FlowSpec = {
  type: "flow",
  nodes: [
    { id: "a", label: "Answer()", kind: "start" },
    { id: "b", label: 'sources "empty"?', kind: "decision" },
    { id: "c", label: "NothingFound", kind: "end" },
    { id: "d", label: "Stream to the model", kind: "step" },
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
    expect(out).toContain("repos_yaml->>repos_yaml_2: Load repository list");
  });

  it("draws call, async and return with their own arrows", () => {
    const out = toMermaid(seq);
    expect(out).toContain("repos_yaml_2-)idx: Sync specs");
    expect(out).toContain("idx-->>repos_yaml_2: Store files");
  });

  it("keeps a self-message on one actor", () => {
    expect(toMermaid(seq)).toContain("idx->>idx: Keyword indexing");
  });

  it("carries no markers, drawn or copied", () => {
    expect(toMermaid(seq)).not.toMatch(/\[\d+\]/);
    // Node lines only: the fixture's edge label "yes [3]" is the model's
    // own text, not a marker toMermaid added.
    const nodes = toMermaid(flow)
      .split("\n")
      .filter((l) => !l.includes("-->"));
    expect(nodes.join("\n")).not.toMatch(/\[\d+\]/);
  });

  it("renames an id mermaid reads as syntax", () => {
    // "end" is the flow spec's own node kind, so it is what a model writes.
    // Left alone it closes a block in a sequence and fails to parse in a
    // flowchart, taking the picture with it.
    const out = toMermaid({
      type: "flow",
      nodes: [
        { id: "start", label: "Begin", kind: "start" },
        { id: "end", label: "Done", kind: "end" },
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
      steps: [{ from: "ui", to: "ui", label: "tick", kind: "call" }],
    });
    expect(out).toContain("participant ui as ui");
    expect(out).not.toMatch(/ as\s*$/m);
  });

  it("writes a flow with the kind in the node shape", () => {
    const out = toMermaid(flow);
    expect(out.split("\n")[0]).toBe("flowchart TD");
    expect(out).toContain('a(["Answer()"])');
    expect(out).toContain('d["Stream to the model"]');
    expect(out).toContain("{");
  });

  it("escapes a quote inside a label rather than ending it early", () => {
    expect(toMermaid(flow)).toContain("#quot;empty#quot;");
  });

  it("quotes an edge label so a marker survives the parser", () => {
    expect(toMermaid(flow)).toContain("a --> b");
    expect(toMermaid(flow)).toContain('b -->|"yes [3]"| c');
  });
});

describe("mermaidize on a stray class mark", () => {
  it("drops it from a mermaid fence, as the drawing does, and leaves the rest", () => {
    const text =
      'Prose.\n\n```mermaid\nflowchart TD\n  a --> b["Price"]:::focus\n:::focus\n```\n\nMore.';
    expect(mermaidize(text)).toBe(
      'Prose.\n\n```mermaid\nflowchart TD\n  a --> b["Price"]:::focus\n```\n\nMore.',
    );
  });
});

describe("mermaidize", () => {
  const body = JSON.stringify({
    type: "sequence",
    actors: [{ id: "ui", label: "Ask.tsx" }],
    steps: [{ from: "ui", to: "ui", label: "render", kind: "call" }],
  });

  it("replaces a diagram fence with a mermaid one", () => {
    const out = mermaidize(
      `Before [1].\n\n\`\`\`diagram\n${body}\n\`\`\`\n\nAfter.`,
    );
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

  it("retags a diagram fence holding mermaid text, which draws here and nowhere else", () => {
    const src = "```diagram\nflowchart TD\n  a --> b\n```\n";
    expect(mermaidize(src)).toBe("```mermaid\nflowchart TD\n  a --> b\n```\n");
  });

  it("leaves an unclosed diagram fence as the text it is", () => {
    const src = "```diagram\n{half";
    expect(mermaidize(src)).toBe(src);
  });
});
