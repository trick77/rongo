import type { ElkExtendedEdge, ElkNode } from "elkjs/lib/elk-api";
import type { Card, ERIR, EntityIR } from "./diagramIR";
import {
  frame,
  labelBox,
  looksLikeCode,
  tidy,
  type EdgeRoute,
  type Elk,
  type FlowLayout,
  type NodeBox,
} from "./diagramLayout";
import { ramp, textWidth, wrap, type Face } from "./diagramTheme";

/**
 * Where an entity model goes: each entity a table, its name in a tinted
 * head and a row per field (type, name, keys, comment in columns), each
 * relationship a square-cornered line between two tables with its role
 * beside it and its cardinality drawn at either end. ELK places the tables
 * and routes the lines, as it does a flow nothing simpler fits.
 *
 * The layout is a flow layout with tables for nodes, so the connector rules
 * a flowchart is held to (diagramCheck.ts check) hold here too.
 */

export type TableRow = {
  cells: {
    text: string[];
    face: Face;
    size: number;
    x: number;
    muted: boolean;
  }[];
  y: number;
  h: number;
};
export type Table = {
  name: { text: string; face: Face; size: number; bold: boolean };
  /** The head's height; rows start under it. */
  head: number;
  rows: TableRow[];
};
export type ERLayout = FlowLayout & {
  tables: Map<string, Table>;
  /** Per edge, the cardinality drawn at its from end and at its to end. */
  cards: { from: Card; to: Card }[];
};

/** Text to frame, each side. */
export const tablePad = 16;
const colGap = 12;
const headH = 36;
const rowLine = 16;
const rowPad = 6;
/** The least width of a table, and the widest a comment runs before it
 * wraps. */
const tableMin = 140;
const commentMax = 160;
/** The room a cardinality mark takes along its line, from the table. */
export const markRoom = 22;

const cell = (
  text: string,
  face: Face,
  size: number,
  muted: boolean,
  max?: number,
) => ({
  text: max ? wrap(text, max, size, face) : [text],
  face,
  size,
  muted,
});

/** table sizes an entity: the columns line up across its rows, each as
 * wide as its widest entry. code sets its name in the mono rather than
 * the bold sans. */
function table(e: EntityIR, code: boolean): { t: Table; w: number; h: number } {
  const name = {
    text: e.label,
    face: (code ? "mono" : "sans") as Face,
    size: code ? ramp.code : ramp.bold,
    bold: !code,
  };
  const nameW = textWidth(name.text, name.size, name.face) + 2 * tablePad;
  if (e.attrs.length === 0) {
    const w = Math.max(tableMin, Math.ceil(nameW));
    return { t: { name, head: 40, rows: [] }, w, h: 40 };
  }
  const grid = e.attrs.map((a) => [
    cell(a.type, "mono", ramp.code, true),
    cell(a.name, "mono", ramp.code, false),
    cell(a.keys.join(","), "mono", ramp.label, true),
    cell(a.comment, "sans", ramp.code, true, commentMax),
  ]);
  const widths = [0, 1, 2, 3].map((c) =>
    Math.max(
      0,
      ...grid.map((r) =>
        Math.max(...r[c].text.map((l) => textWidth(l, r[c].size, r[c].face))),
      ),
    ),
  );
  // A column nobody fills takes no room, nor the gap before it.
  const xs: number[] = [];
  let x = tablePad;
  widths.forEach((w, c) => {
    xs.push(x);
    if (w > 0 || c < 2) x += Math.ceil(w) + colGap;
  });
  const w = Math.max(tableMin, Math.ceil(nameW), x - colGap + tablePad);
  let y = headH + rowPad / 2;
  const rows: TableRow[] = grid.map((r) => {
    const h = Math.max(...r.map((c) => c.text.length)) * rowLine + rowPad;
    const row = {
      cells: r
        .map((c, k) => ({ ...c, x: xs[k] }))
        .filter((c) => c.text.some((l) => l !== "")),
      y,
      h,
    };
    y += h;
    return row;
  });
  return { t: { name, head: headH, rows }, w, h: Math.ceil(y + rowPad / 2) };
}

export async function layoutER(ir: ERIR, elk: Elk): Promise<ERLayout> {
  const tables = new Map<string, Table>();
  const boxes = new Map<string, NodeBox>();
  // One face for every table's name: decided per name, a row of class
  // names read DorChecklist in the mono beside Verdict in the bold sans.
  const code = ir.entities.some((e) => looksLikeCode(e.label));
  for (const e of ir.entities) {
    const { t, w, h } = table(e, code);
    tables.set(e.id, t);
    boxes.set(e.id, {
      id: e.id,
      kind: "step",
      lines: [],
      face: t.name.face,
      size: t.name.size,
      bold: t.name.bold,
      focus: e.focus ?? false,
      x: 0,
      y: 0,
      w,
      h,
    });
  }
  const labels = ir.rels.map((r) =>
    r.label !== "" ? labelBox(r.label) : null,
  );
  const children: ElkNode[] = ir.entities.map((e) => ({
    id: e.id,
    width: boxes.get(e.id)!.w,
    height: boxes.get(e.id)!.h,
  }));
  const edges: ElkExtendedEdge[] = ir.rels.map((r, i) => ({
    id: `e${i}`,
    sources: [r.from],
    targets: [r.to],
    ...(labels[i]
      ? {
          labels: [
            {
              id: `e${i}l`,
              text: r.label,
              width: labels[i]!.w,
              height: labels[i]!.h,
            },
          ],
        }
      : {}),
  }));
  const root = await elk.layout({
    id: "root",
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": ir.direction === "TB" ? "DOWN" : "RIGHT",
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.json.edgeCoords": "ROOT",
      "elk.padding": "[top=0,left=0,bottom=0,right=0]",
      // Wider than a flow's: a line's two ends carry a mark each, and its
      // middle a role, between tables that are themselves wide.
      "elk.spacing.nodeNode": "40",
      "elk.layered.spacing.nodeNodeBetweenLayers": "64",
      "elk.spacing.edgeNode": "24",
      "elk.spacing.edgeEdge": "16",
      "elk.layered.spacing.edgeEdgeBetweenLayers": "16",
      "elk.layered.spacing.edgeNodeBetweenLayers": "28",
      "elk.spacing.edgeLabel": "10",
      "elk.edgeLabels.placement": "CENTER",
      "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
      "elk.layered.nodePlacement.strategy": "BRANDES_KOEPF",
      "elk.layered.nodePlacement.bk.fixedAlignment": "BALANCED",
    },
    children,
    edges,
  });
  for (const c of root.children ?? []) {
    const b = boxes.get(c.id)!;
    b.x = Math.round(c.x ?? 0);
    b.y = Math.round(c.y ?? 0);
  }
  const routes: EdgeRoute[] = (root.edges ?? []).map((e, i) => {
    const r = ir.rels[i];
    const sec = e.sections?.[0];
    const points = tidy(
      sec
        ? [sec.startPoint, ...(sec.bendPoints ?? []), sec.endPoint].map(
            (p) => ({
              x: Math.round(p.x),
              y: Math.round(p.y),
            }),
          )
        : [],
    );
    const route: EdgeRoute = {
      from: r.from,
      to: r.to,
      points,
      dashed: r.dashed,
      warn: false,
      head: "none",
    };
    const l = e.labels?.[0];
    if (l && labels[i])
      route.label = {
        ...labels[i]!,
        x: Math.round(l.x ?? 0),
        y: Math.round(l.y ?? 0),
      };
    return route;
  });
  const nodes = ir.entities.map((e) => boxes.get(e.id)!);
  return {
    ...frame(nodes, routes, []),
    tables,
    cards: ir.rels.map((r) => ({ from: r.fromCard, to: r.toCard })),
  };
}
