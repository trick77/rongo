import { describe, it, expect, beforeAll } from "vitest";
import mermaid from "mermaid";
import type { DiagramIR } from "@trick77/diagram";
import {
  diagramKind,
  dropStrayMarks,
  editorial,
  escapeAngles,
  fits,
  ownKinds,
  toIR,
} from "@trick77/diagram/mermaid";
import { checkIR } from "@trick77/diagram/check";
import { diagramSource } from "./diagram";
import { fenceRe } from "./markdown";
import { mermaidize } from "./diagramExport";

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
 * read as markdown.tsx reads it, with the stray class marks draw() and the
 * clipboard drop already dropped (dropStrayMarks). The fence as the backend
 * stored it is checked on its own below. */
function fence(text: string): { tag: string; body: string } | null {
  const f = storedFence(text);
  return f && { tag: f.tag, body: dropStrayMarks(f.body) };
}

function storedFence(text: string): { tag: string; body: string } | null {
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
      const stored = storedFence(text);
      expect(stored, "the backend left no diagram fence").not.toBeNull();
      // The fence as stored parses, or a stray class mark is all that stands
      // in its way: that is the one repair draw() and the clipboard make, and
      // anything else refused here is a defect neither of them hides.
      const f = fence(text)!;
      if (!(await mermaid.parse(stored!.body, { suppressErrors: true })))
        expect(f.body, `the renderer refuses:\n${stored!.body}`).not.toBe(
          stored!.body,
        );
      const ok = await mermaid.parse(f.body, { suppressErrors: true });
      expect(ok, `the renderer refuses:\n${f.body}`).toBeTruthy();
      // What Copy as Markdown hands another renderer parses too.
      const copied = storedFence(mermaidize(text))!;
      expect(
        await mermaid.parse(copied.body, { suppressErrors: true }),
        `the copied fence is refused:\n${copied.body}`,
      ).toBeTruthy();
      // draw() parses the source with its brackets kept as text; one it
      // cannot parse falls back and loses them, which must show here.
      const escaped = escapeAngles(f.body);
      const okEscaped = await mermaid.parse(escaped, { suppressErrors: true });
      expect(
        okEscaped,
        `the escaped source is refused:\n${escaped}`,
      ).toBeTruthy();
    });
  }
});

/** labels is every text an IR draws, for the bracket count. */
function labels(ir: DiagramIR): string[] {
  if (ir.kind === "sequence")
    return [
      ...ir.actors.map((a) => a.label),
      ...ir.rows.map((r) => ("label" in r ? r.label : "")),
    ];
  if (ir.kind === "er")
    return [
      ...ir.entities.flatMap((e) => [
        e.label,
        ...e.attrs.flatMap((a) => [a.type, a.name, a.comment]),
      ]),
      ...ir.rels.map((r) => r.label),
    ];
  if (ir.kind === "class")
    return [
      ...ir.classes.flatMap((c) => [
        c.label,
        c.stereotype ?? "",
        ...[...c.fields, ...c.methods].map((m) => m.text),
      ]),
      ...ir.rels.flatMap((r) => [r.label, r.fromCard, r.toCard]),
    ];
  if (ir.kind === "flow" || ir.kind === "swimlane")
    return [
      ...ir.nodes.map((n) => n.label),
      ...ir.edges.map((e) => e.label),
      ...(ir.kind === "swimlane" ? ir.lanes : ir.zones).map((z) => z.title),
    ];
  // A kind no answer here is asked for (mindmap, timeline, quadrant): the
  // package's own corpus counts its brackets.
  return [];
}

/** generics counts the angle brackets a class diagram's `~T~` becomes,
 * which escapeAngles never saw: a tilde in a class's declaration or body is
 * drawn as one. One in a relationship's class name, `Repo~T~ <|-- Store`,
 * only names the class. As the package's corpus counts them. */
function generics(src: string): number {
  if (!diagramKind(src).startsWith("classDiagram")) return 0;
  let inClass = false;
  let n = 0;
  for (const line of src.split("\n")) {
    const t = line.trim();
    if (inClass) {
      if (t.startsWith("}")) inClass = false;
      else n += (t.match(/~/g) ?? []).length;
    } else if (/^class\s/.test(t)) {
      n += (t.match(/~/g) ?? []).length;
      inClass = t.endsWith("{");
    }
  }
  return n;
}

/** shapes is the drawing a source must come out as where the fence alone
 * does not say: a swimlane and a mapping are both a flowchart LR with
 * subgraphs, and a swimlane the package declines is drawn as a plain flow
 * without failing anything else here. The backend's tally reads the same
 * files (renumber.go DiagramShape) and must agree. */
const shapes: Record<string, string> = {
  "eval-swimlane-checkout-en.golden": "swimlane",
  "mapping-lr.golden": "flow",
  "eval-class-payment-en.golden": "class",
};

describe("rongo's drawing of the corpus", () => {
  beforeAll(() => {
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict" });
  });

  it("reads the shapes the prompt asks for as those shapes", async () => {
    for (const [file, want] of Object.entries(shapes)) {
      const text = Object.entries(goldens).find(([p]) =>
        p.endsWith(`/${file}`),
      )?.[1];
      expect(text, `${file} is missing from the corpus`).toBeDefined();
      const src = escapeAngles(fence(text!)!.body);
      const d = await mermaid.mermaidAPI.getDiagramFromText(src);
      const ir = toIR(d.type, d.db);
      expect(ir?.kind, file).toBe(want);
      expect(fits(ir!), `${file} is past the caps`).toBe(true);
    }
  });

  for (const [path, text] of Object.entries(goldens)) {
    const name = path.slice(path.lastIndexOf("/") + 1);
    // Which drawing each answer gets: rongo's own for the kinds it draws
    // (ownKinds), the renderer's for the rest. Asserted per answer, so a
    // diagram that quietly falls back to the renderer fails here rather
    // than looking like it always did.
    it(`draws ${name} with the drawing its kind gets`, async () => {
      const src = fence(text)!.body;
      const svg = await editorial(mermaid, escapeAngles(src), "corpus");
      if (ownKinds.has(diagramKind(src))) {
        expect(svg, "rongo declined a kind it draws").toMatch(/^<svg /);
      } else {
        expect(svg, "rongo drew what the renderer should").toBeNull();
      }
    });

    it(`keeps every angle bracket ${name}'s labels hold`, async () => {
      const src = fence(text)!.body;
      if (!ownKinds.has(diagramKind(src))) return;
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
      const drawn = labels(ir).join("");
      expect((drawn.match(/[<>]/g) ?? []).length).toBe(kept + generics(src));
      expect(drawn).not.toMatch(/#lt;|#gt;|ﬂ°|¶ß/);
    });

    it(`lays out ${name} by the connector rules`, async () => {
      const src = fence(text)!.body;
      const d = await mermaid.mermaidAPI.getDiagramFromText(escapeAngles(src));
      const ir = toIR(d.type, d.db);
      if (ir === null || !fits(ir)) return;
      // Laid out as the drawing lays it out, whatever the kind.
      expect(await checkIR(ir)).toEqual([]);
    });

    it(`frames every node ${name} marks :::focus, and no other`, async () => {
      const src = fence(text)!.body;
      if (diagramKind(src) !== "flowchart" && diagramKind(src) !== "graph")
        return;
      const d = await mermaid.mermaidAPI.getDiagramFromText(escapeAngles(src));
      const ir = toIR(d.type, d.db);
      if (ir === null || (ir.kind !== "flow" && ir.kind !== "swimlane")) return;
      // A node marked where it is declared, `c["…"]:::focus`, by its id.
      const marked = new Set(
        [
          ...src.matchAll(
            /(\w+)\s*(?:\[[^\]]*\]|\{[^}]*\}|\([^)]*\))?:::focus\b/g,
          ),
        ].map((m) => m[1]),
      );
      expect(
        ir.nodes
          .filter((n) => n.focus)
          .map((n) => n.id)
          .sort(),
      ).toEqual([...marked].sort());
    });
  }
});
