import { describe, it, expect, beforeAll } from "vitest";
import mermaid from "mermaid";
import ELK from "elkjs/lib/elk.bundled.js";
import { diagramKind, diagramSource } from "./diagram";
import { escapeAngles } from "./diagramText";
import { fenceRe } from "./markdown";
import { fits, toIR } from "./diagramIR";
import { layoutFlow } from "./diagramLayout";
import { check } from "./diagramCheck";
import { editorial } from "./editorial";

/**
 * The other half of the diagram corpus. The .txt files are answers as models
 * actually wrote them, the .golden files are what the backend hands the
 * browser (backend/internal/ask/renumber_corpus_test.go writes them with
 * `go test ./internal/ask/ -update`).
 *
 * Both ends read the same files on purpose. An answer the backend passes and
 * this end will not draw is the same defect as one neither touches, and it
 * was never caught because each end had its own hand-written examples.
 *
 * The renderer's parser is the real one, not a mock: parse runs without a
 * DOM, and whether it accepts the source is exactly the question. Drawing
 * it needs a browser, which is what the Playwright check is for.
 *
 * A diagram that fails to draw belongs here as a new .txt first, and the fix
 * afterwards.
 */

const goldens: Record<string, string> = import.meta.glob(
  "../../backend/internal/ask/testdata/diagrams/*.golden",
  { query: "?raw", import: "default", eager: true },
);

/** fence returns the tag and body of the one diagram fence in an answer,
 * read as markdown.tsx reads it. */
function fence(text: string): { tag: string; body: string } | null {
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const open = fenceRe.exec(lines[i]);
    if (!open) continue;
    const body: string[] = [];
    for (i++; i < lines.length && !fenceRe.test(lines[i]); i++)
      body.push(lines[i]);
    const src = diagramSource(open[1], body.join("\n"));
    if (src !== null) return { tag: open[1], body: src };
  }
  return null;
}

describe("the diagram corpus", () => {
  it("is not empty, or the two ends stopped sharing it", () => {
    expect(Object.keys(goldens).length).toBeGreaterThan(0);
  });

  for (const [path, text] of Object.entries(goldens)) {
    const name = path.slice(path.lastIndexOf("/") + 1);
    it(`draws ${name}`, async () => {
      const f = fence(text);
      expect(f, "the backend left no diagram fence").not.toBeNull();
      const ok = await mermaid.parse(f!.body, { suppressErrors: true });
      expect(ok, `the renderer refuses:\n${f!.body}`).toBeTruthy();
      // draw() parses the source with its brackets kept as text; one it
      // cannot parse falls back and loses them, which must show here.
      const escaped = escapeAngles(f!.body);
      const okEscaped = await mermaid.parse(escaped, { suppressErrors: true });
      expect(
        okEscaped,
        `the escaped source is refused:\n${escaped}`,
      ).toBeTruthy();
    });
  }
});

/** Which drawing each kind gets. A flowchart is rongo's own; every other
 * kind is the renderer's until rongo draws it. Asserted per answer, so a
 * flowchart that quietly falls back to the renderer fails here rather than
 * looking like it always did. */
const own = new Set(["flowchart", "graph"]);

describe("rongo's drawing of the corpus", () => {
  beforeAll(() => {
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict" });
  });

  for (const [path, text] of Object.entries(goldens)) {
    const name = path.slice(path.lastIndexOf("/") + 1);
    it(`draws ${name} with the drawing its kind gets`, async () => {
      const src = fence(text)!.body;
      const svg = await editorial(mermaid, escapeAngles(src), "corpus");
      if (own.has(diagramKind(src))) {
        expect(svg, "rongo declined a flowchart").toMatch(/^<svg /);
      } else {
        expect(svg, "rongo drew a kind it has no drawing for").toBeNull();
      }
    });

    it(`keeps every angle bracket ${name}'s labels hold`, async () => {
      const src = fence(text)!.body;
      if (!own.has(diagramKind(src))) return;
      // Every bracket the escape turned into text reaches a drawn label, and
      // nothing of the escaping is left in one.
      const escaped = escapeAngles(src);
      // Tooltips, links and styles are escaped too but never drawn as a label.
      const labelled = escaped
        .split("\n")
        .filter((l) => !/^\s*(click|classDef|class|style|linkStyle)\b/.test(l))
        .join("\n");
      const kept = (labelled.match(/#lt;|#gt;/g) ?? []).length;
      const d = await mermaid.mermaidAPI.getDiagramFromText(escaped);
      const ir = toIR(d.type, d.db);
      if (ir === null) return;
      const drawn = [
        ...ir.nodes.map((n) => n.label),
        ...ir.edges.map((e) => e.label),
        ...ir.zones.map((z) => z.title),
      ].join("");
      expect((drawn.match(/[<>]/g) ?? []).length).toBe(kept);
      expect(drawn).not.toMatch(/#lt;|#gt;|ﬂ°|¶ß/);
    });

    it(`lays out ${name} by the connector rules`, async () => {
      const src = fence(text)!.body;
      const d = await mermaid.mermaidAPI.getDiagramFromText(escapeAngles(src));
      const ir = toIR(d.type, d.db);
      if (ir === null || !fits(ir)) return;
      expect(check(await layoutFlow(ir, new ELK()))).toEqual([]);
    });
  }
});
