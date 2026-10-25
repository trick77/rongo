import { describe, it, expect } from "vitest";
import { escapeAngles } from "./diagramText";

describe("escapeAngles in a flowchart", () => {
  it("keeps brackets in quoted labels as text, arrows and line breaks as they are", () => {
    expect(
      escapeAngles(
        'flowchart LR\n  m2["Failed to fetch <url>: <cause>"]\n  a -->|"x <n> y"| b["one<br/>two"]',
      ),
    ).toBe(
      'flowchart LR\n  m2["Failed to fetch #lt;url#gt;: #lt;cause#gt;"]\n  a -->|"x #lt;n#gt; y"| b["one<br/>two"]',
    );
  });

  it("finds a quoted label's end across lines, never escaping the arrow after it", () => {
    expect(
      escapeAngles('flowchart LR\n  a["`multi\nline <x>`"] --> b["y"]'),
    ).toBe('flowchart LR\n  a["`multi\nline #lt;x#gt;`"] --> b["y"]');
  });

  it("escapes an unquoted edge label between pipes", () => {
    expect(escapeAngles("flowchart LR\n  a -->|retries < 3| b")).toBe(
      "flowchart LR\n  a -->|retries #lt; 3| b",
    );
  });

  it("keeps the formatting tags the renderer honours as markup", () => {
    expect(escapeAngles('flowchart TD\n  a["<b>Retry</b> <n> times"]')).toBe(
      'flowchart TD\n  a["<b>Retry</b> #lt;n#gt; times"]',
    );
  });

  it("leaves a source without angle brackets byte for byte", () => {
    const src = 'flowchart TD\n  a["x"] --> b{"y?"}';
    expect(escapeAngles(src)).toBe(src);
  });
});

describe("escapeAngles in a sequence", () => {
  it("escapes a message after its colon, never the arrow", () => {
    expect(escapeAngles("sequenceDiagram\n  A->>B: List<Order> back")).toBe(
      "sequenceDiagram\n  A->>B: List#lt;Order#gt; back",
    );
    expect(escapeAngles("sequenceDiagram\n  A<<->>B: x<y>")).toBe(
      "sequenceDiagram\n  A<<->>B: x#lt;y#gt;",
    );
  });

  it("escapes the text after a block keyword or a participant's alias", () => {
    expect(
      escapeAngles(
        "sequenceDiagram\n  participant R as Repo<T>\n  alt retries < 3\n  loop until n > 0\n  end",
      ),
    ).toBe(
      "sequenceDiagram\n  participant R as Repo#lt;T#gt;\n  alt retries #lt; 3\n  loop until n #gt; 0\n  end",
    );
  });

  it("knows the kind past a directive and a blank line", () => {
    expect(
      escapeAngles(
        '%%{init: {"theme":"base"}}%%\n\nsequenceDiagram\n  A->>B: List<Order>',
      ),
    ).toBe(
      '%%{init: {"theme":"base"}}%%\n\nsequenceDiagram\n  A->>B: List#lt;Order#gt;',
    );
  });
});

describe("escapeAngles in state, class and entity diagrams", () => {
  it("escapes a transition's label and a note's body", () => {
    expect(
      escapeAngles(
        "stateDiagram-v2\n  A --> B : retries < 3\n  note right of B\n    waits for <ack>\n  end note",
      ),
    ).toBe(
      "stateDiagram-v2\n  A --> B : retries #lt; 3\n  note right of B\n    waits for #lt;ack#gt;\n  end note",
    );
  });

  it("escapes a relation's label after its colon", () => {
    expect(
      escapeAngles("classDiagram\n  Repo --> Store : returns List<Item>"),
    ).toBe("classDiagram\n  Repo --> Store : returns List#lt;Item#gt;");
    expect(escapeAngles("erDiagram\n  A ||--o{ B : has <many>")).toBe(
      "erDiagram\n  A ||--o{ B : has #lt;many#gt;",
    );
  });
});
