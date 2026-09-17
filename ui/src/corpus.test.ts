import { describe, it, expect } from "vitest";
import mermaid from "mermaid";
import { diagramSource } from "./diagram";
import { fenceRe } from "./markdown";

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
    for (i++; i < lines.length && !fenceRe.test(lines[i]); i++) body.push(lines[i]);
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
    });
  }
});
