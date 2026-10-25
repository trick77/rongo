import ELK from "elkjs/lib/elk.bundled.js";
import { fits, toIR } from "./diagramIR";
import { layoutFlow, type Elk } from "./diagramLayout";
import { drawFlow } from "./diagramDraw";
import { palette } from "./diagramTheme";

/**
 * rongo's own drawing of a diagram source, or null when it has none and the
 * renderer should draw the source as it always did. Loaded by diagram.tsx
 * on the first fence, as its own chunk: the layout engine is large and a
 * page drawing no diagram should not carry it.
 */

/** The part of the renderer this reads: its parse, as a type and a db. */
export type Parser = {
  mermaidAPI: {
    getDiagramFromText(src: string): Promise<{ type: string; db: unknown }>;
  };
};

let elk: Elk | null = null;

export async function editorial(
  mermaid: Parser,
  src: string,
  ids: string,
): Promise<string | null> {
  const d = await mermaid.mermaidAPI.getDiagramFromText(src);
  const ir = toIR(d.type, d.db);
  if (ir === null || !fits(ir)) return null;
  elk ??= new ELK();
  return drawFlow(await layoutFlow(ir, elk), palette(), ids);
}
