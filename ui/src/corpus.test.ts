import { describe, it, expect } from "vitest";
import { parseDiagram } from "./diagram";

/**
 * The other half of the diagram corpus. The .txt files are answers as models
 * actually wrote them, the .golden files are what the backend hands the
 * browser (backend/internal/ask/renumber_corpus_test.go writes them with
 * `go test ./internal/ask/ -update`).
 *
 * Both ends read the same files on purpose. A spec the backend normalises and
 * this end will not draw is the same defect as one neither touches, and it
 * was never caught because each end had its own hand-written examples.
 *
 * A diagram that fails to draw belongs here as a new .txt first, and the fix
 * afterwards.
 */

const goldens: Record<string, string> = import.meta.glob(
  "../../backend/internal/ask/testdata/diagrams/*.golden",
  { query: "?raw", import: "default", eager: true },
);

/** fence returns the body of the one `diagram` fence in an answer. */
function fence(text: string): string | null {
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].trim() !== "```diagram") continue;
    const body: string[] = [];
    for (i++; i < lines.length && !lines[i].trim().startsWith("```"); i++) body.push(lines[i]);
    return body.join("\n");
  }
  return null;
}

describe("the diagram corpus", () => {
  it("is not empty, or the two ends stopped sharing it", () => {
    expect(Object.keys(goldens).length).toBeGreaterThan(0);
  });

  for (const [path, text] of Object.entries(goldens)) {
    const name = path.slice(path.lastIndexOf("/") + 1);
    it(`draws ${name}`, () => {
      const body = fence(text);
      expect(body, "the backend left no ```diagram fence").not.toBeNull();

      const spec = parseDiagram(body as string);
      expect(spec, "the backend normalised it and the renderer will not draw it").not.toBeNull();

      // Every chip stands on a number the backend renumbered. Anything else
      // is a prompt index, and it would name a different file than the node
      // rests on.
      const cited =
        spec!.type === "flow" ? spec!.nodes.map((n) => n.src) : spec!.steps.map((s) => s.src);
      for (const src of cited) {
        for (const m of src) expect(Number.isInteger(m) && m > 0).toBe(true);
      }
    });
  }
});
