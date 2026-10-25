import { beforeAll, describe, it, expect } from "vitest";
import mermaid from "mermaid";
import { toIR, fits, type FlowIR, type SequenceIR } from "./diagramIR";

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

async function sequence(src: string): Promise<SequenceIR> {
  const out = await ir(src);
  expect(out).not.toBeNull();
  expect(out!.kind).toBe("sequence");
  return out as SequenceIR;
}

describe("toIR for a sequence", () => {
  it("reads participants in order, by their alias", async () => {
    const s = await sequence(`sequenceDiagram
  participant UI as Ask.tsx
  actor U
  UI->>U: hi
  U->>Z: undeclared`);
    expect(s.actors).toEqual([
      { id: "UI", label: "Ask.tsx" },
      { id: "U", label: "U" },
      { id: "Z", label: "Z" },
    ]);
  });

  it("says how each arrow is drawn", async () => {
    const s = await sequence(`sequenceDiagram
  A->>B: call
  B-->>A: back
  A-)B: async
  A--)B: async back
  A->B: open
  A-->B: dotted open
  A-xB: lost
  A--xB: lost back
  A<<->>B: both
  A<<-->>B: both back`);
    expect(
      s.rows.map((r) => (r.type === "message" ? [r.line, r.head] : r.type)),
    ).toEqual([
      ["solid", "filled"],
      ["dashed", "filled"],
      ["solid", "open"],
      ["dashed", "open"],
      ["solid", "none"],
      ["dashed", "none"],
      ["solid", "cross"],
      ["dashed", "cross"],
      ["solid", "both"],
      ["dashed", "both"],
    ]);
  });

  it("keeps a message's text, a self message and a note", async () => {
    const s = await sequence(`sequenceDiagram
  participant A
  participant B
  A->>A: think #amp; wait
  Note over A,B: spans both
  Note left of A: left
  Note right of B: right<br/>two`);
    expect(s.rows).toEqual([
      {
        type: "message",
        from: "A",
        to: "A",
        label: "think & wait",
        line: "solid",
        head: "filled",
      },
      { type: "note", from: "A", to: "B", side: "over", label: "spans both" },
      { type: "note", from: "A", to: "A", side: "left", label: "left" },
      { type: "note", from: "B", to: "B", side: "right", label: "right\ntwo" },
    ]);
  });

  it("reads frames as a start, its dividers and an end", async () => {
    const s = await sequence(`sequenceDiagram
  par fetch
    A->>B: one
  and
    A->>C: two
  end
  alt ok
    B-->>A: yes
  else failed
    B-->>A: no
  end
  loop every 5s
    A->>B: ping
  end
  opt cached
    A->>B: hit
  end
  critical lock
    A->>B: take
  option timeout
    A->>B: give up
  end
  break empty
    A->>B: stop
  end`);
    expect(
      s.rows
        .filter((r) => r.type !== "message" && r.type !== "note")
        .map((r) =>
          r.type === "end" ? "end" : `${r.type} ${r.keyword} ${r.label}`,
        ),
    ).toEqual([
      "frame par fetch",
      "divider and ",
      "end",
      "frame alt ok",
      "divider else failed",
      "end",
      "frame loop every 5s",
      "end",
      "frame opt cached",
      "end",
      "frame critical lock",
      "divider option timeout",
      "end",
      "frame break empty",
      "end",
    ]);
  });

  it("ignores activations, which change nothing it draws", async () => {
    const s = await sequence(`sequenceDiagram
  A->>+B: go
  activate A
  B-->>-A: done
  deactivate A`);
    expect(s.rows.map((r) => r.type)).toEqual(["message", "message"]);
  });

  it("draws a sequence whose numbering is switched off", async () => {
    const s = await sequence(`sequenceDiagram
  autonumber off
  A->>B: x`);
    expect(s.rows.map((r) => r.type)).toEqual(["message"]);
  });

  it("reads frames inside frames, three deep", async () => {
    const s = await sequence(`sequenceDiagram
  loop again
    alt ok
      opt cached
        A->>B: x
      end
    else failed
      B-->>A: y
    end
  end`);
    expect(s.rows.map((r) => r.type)).toEqual([
      "frame",
      "frame",
      "frame",
      "message",
      "end",
      "divider",
      "message",
      "end",
      "end",
    ]);
  });

  it("declines frames four deep", async () => {
    expect(
      await ir(`sequenceDiagram
  loop a
    loop b
      loop c
        loop d
          A->>B: x
        end
      end
    end
  end`),
    ).toBeNull();
  });

  it("declines what it has no drawing for", async () => {
    const declined = [
      "rect rgb(1, 2, 3)\n  A->>B: x\n  end",
      "autonumber\n  A->>B: x",
      "box Aqua Group\n  participant A\n  end\n  A->>B: x",
      "A->>B: x\n  create participant C\n  B->>C: hi",
      "par_over both\n  A->>B: x\n  end",
      "title Checkout\n  A->>B: x",
    ];
    for (const body of declined) {
      expect(await ir(`sequenceDiagram\n  ${body}`), body).toBeNull();
    }
  });
});

describe("fits a sequence", () => {
  const src = (actors: number, rows: number) =>
    "sequenceDiagram\n" +
    Array.from({ length: actors }, (_, i) => `  participant p${i}`).join("\n") +
    "\n" +
    Array.from({ length: rows }, (_, i) => `  p0->>p1: m${i}`).join("\n");

  it("draws up to eight participants and twenty rows", async () => {
    expect(fits(await sequence(src(8, 20)))).toBe(true);
    expect(fits(await sequence(src(9, 2)))).toBe(false);
    expect(fits(await sequence(src(2, 21)))).toBe(false);
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
    expect(toIR("sequence", {})).toBeNull();
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
