import { beforeAll, describe, it, expect } from "vitest";
import mermaid from "mermaid";
import { toIR, fits, type FlowIR } from "./diagramIR";

// The renderer registers its diagram types on initialize; diagram.tsx does
// the same before its first parse.
beforeAll(() => {
  mermaid.initialize({ startOnLoad: false, securityLevel: "strict" });
});

async function ir(src: string) {
  const d = await mermaid.mermaidAPI.getDiagramFromText(src);
  return toIR(d.type, d.db);
}

async function flow(src: string): Promise<FlowIR> {
  const out = await ir(src);
  expect(out).not.toBeNull();
  expect(out!.kind).toBe("flow");
  return out as FlowIR;
}

describe("toIR for a flowchart", () => {
  it("reads nodes in the order they were written, with their shape", async () => {
    const f = await flow(`flowchart TD
  a(["Answer()"]) --> b{"sources empty?"}
  b -->|"yes"| c(["NothingFound"])
  b --> d["Stream"]
  d --> e(("dot"))
  e --> f[(Store)]`);
    expect(f.direction).toBe("TB");
    expect(f.nodes.map((n) => [n.id, n.label, n.shape])).toEqual([
      ["a", "Answer()", "pill"],
      ["b", "sources empty?", "decision"],
      ["c", "NothingFound", "pill"],
      ["d", "Stream", "step"],
      ["e", "dot", "pill"],
      ["f", "Store", "step"],
    ]);
  });

  it("keeps edge labels and says how the edge is drawn", async () => {
    const f = await flow(`flowchart LR
  a --> b
  b -- Nein --> c
  c -.-> d
  d --- e
  e <--> a
  a ~~~ c`);
    expect(f.direction).toBe("LR");
    expect(f.edges).toEqual([
      { from: "a", to: "b", label: "", dashed: false, head: "end" },
      { from: "b", to: "c", label: "Nein", dashed: false, head: "end" },
      { from: "c", to: "d", label: "", dashed: true, head: "end" },
      { from: "d", to: "e", label: "", dashed: false, head: "none" },
      { from: "e", to: "a", label: "", dashed: false, head: "both" },
    ]);
  });

  it("takes a node with no label as its id, and a bare id as a step", async () => {
    const f = await flow(`flowchart TD
  start --> stop`);
    expect(f.nodes).toEqual([
      { id: "start", label: "start", shape: "step" },
      { id: "stop", label: "stop", shape: "step" },
    ]);
  });

  it("marks a node the model tagged as the focus", async () => {
    const f = await flow(`flowchart TD
  a["Gather"] --> b["Rerank"]:::focus
  classDef focus stroke-width:2px`);
    expect(f.nodes.map((n) => n.focus ?? false)).toEqual([false, true]);
  });

  it("shows entities and markdown as mermaid's own drawing did", async () => {
    const f = await flow(`flowchart TD
  a["Tom &amp; Jerry &lt; 3"] --> b["say #quot;hi#quot; #35;1"]
  b -- "a &amp; b" --> c[\`**bold** md\`]
  subgraph s ["R&amp;D"]
    d
  end`);
    expect(f.nodes.map((n) => n.label)).toEqual([
      "Tom & Jerry < 3",
      'say "hi" #1',
      "bold md",
      "d",
    ]);
    expect(f.edges[1].label).toBe("a & b");
    expect(f.zones[0].title).toBe("R&D");
  });

  it("keeps no zone for a subgraph holding no node", async () => {
    const f = await flow(`flowchart TD
  a --> b
  subgraph empty
  end`);
    expect(f.zones).toEqual([]);
  });

  it("reads a line break as a line break and drops markup", async () => {
    const f = await flow(`flowchart TD
  a["one<br/>two <b>bold</b>"]`);
    expect(f.nodes[0].label).toBe("one\ntwo bold");
  });

  it("puts subgraph members in a zone named by its title", async () => {
    const f = await flow(`flowchart LR
  subgraph Ereignis
    e1["Antrag"]
  end
  subgraph t [Topic]
    t1["Benachrichtigung"]
  end
  e1 --> t1`);
    expect(f.zones).toEqual([
      { id: "Ereignis", title: "Ereignis" },
      { id: "t", title: "Topic" },
    ]);
    expect(f.nodes.map((n) => n.zone)).toEqual(["Ereignis", "t"]);
  });

  it("declines bottom-up and right-to-left rather than drawing their mirror", async () => {
    expect(await ir("flowchart BT\n  base --> top")).toBeNull();
    expect(await ir("flowchart RL\n  a --> b")).toBeNull();
  });

  it("declines a subgraph inside a subgraph", async () => {
    expect(
      await ir(`flowchart TD
  subgraph outer
    subgraph inner
      a
    end
  end`),
    ).toBeNull();
  });

  it("declines an edge to a subgraph", async () => {
    expect(
      await ir(`flowchart TD
  subgraph s
    a
  end
  b --> s`),
    ).toBeNull();
  });
});

describe("toIR for what it does not draw", () => {
  it("is null for a kind it has no drawer for", async () => {
    expect(
      await ir(`classDiagram
  class A`),
    ).toBeNull();
  });

  it("is null for a db that is not the shape it reads", () => {
    expect(toIR("flowchart-v2", {})).toBeNull();
    expect(toIR("flowchart-v2", null)).toBeNull();
  });
});

describe("fits", () => {
  it("draws up to sixteen nodes and hands larger ones on", async () => {
    const lines = (n: number) =>
      Array.from({ length: n }, (_, i) => `  n${i} --> n${i + 1}`).join("\n");
    expect(fits(await flow(`flowchart TD\n${lines(15)}`))).toBe(true);
    expect(fits(await flow(`flowchart TD\n${lines(16)}`))).toBe(false);
  });
});
