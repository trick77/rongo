import { fits, toIR } from "./diagramIR";
import { layoutFlow, type Elk } from "./diagramLayout";
import { drawFlow, drawSequence } from "./diagramDraw";
import { layoutSequence } from "./diagramSequence";
import { palette } from "./diagramTheme";

/**
 * rongo's own drawing of a diagram source, or null when it has none and the
 * renderer should draw the source as it always did. Loaded by diagram.tsx
 * on the first fence, as its own chunk. The layout engine is a chunk of its
 * own again, loaded on the first flowchart: it is large, and a sequence
 * never uses it.
 */

/** The part of the renderer this reads: its parse, as a type and a db. */
export type Parser = {
  mermaidAPI: {
    getDiagramFromText(src: string): Promise<{ type: string; db: unknown }>;
  };
};

let elk: Promise<Elk> | null = null;

export async function editorial(
  mermaid: Parser,
  src: string,
  ids: string,
): Promise<string | null> {
  const d = await mermaid.mermaidAPI.getDiagramFromText(src);
  const ir = toIR(d.type, d.db);
  if (ir === null || !fits(ir)) return null;
  if (ir.kind === "sequence")
    return drawSequence(layoutSequence(ir), palette(), ids);
  elk ??= import("elkjs/lib/elk.bundled.js").then(
    (m) => new m.default(),
    (e: unknown) => {
      // A chunk gone after a redeploy is tried again by the next flowchart.
      elk = null;
      throw e;
    },
  );
  return drawFlow(await layoutFlow(ir, await elk), palette(), ids);
}
