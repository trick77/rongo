import type { ReactNode } from "react";
import { fileName } from "@trick77/diagram";
import {
  configure,
  diagramKind,
  diagramTitle,
  draw,
  type Drawn,
} from "@trick77/diagram/mermaid";
import { DiagramCard } from "@trick77/diagram/react";
import { useResolved } from "./useResolved";
import { toMermaid } from "./diagramExport";
import OverlayShell from "./OverlayShell";
import { DownloadIcon, ExpandIcon } from "./icons";

/**
 * One diagram in an answer: a fenced block tagged `mermaid`, drawn by
 * @trick77/diagram into an SVG string that is then set as innerHTML. The
 * package draws flowcharts, sequences, state machines and entity models
 * itself and hands every other kind to the mermaid renderer; why that is
 * safe to set as innerHTML is said there (./mermaid draw).
 *
 * The picture no longer cites: a node carries no markers and draws no chip.
 * The sentence that introduces the diagram names its sources, as a doc-only
 * claim already does. What it bought is the type table: a flowchart, a
 * sequence, a state machine, an entity model or a trigger-to-target mapping,
 * chosen by the shape of the answer rather than forced into the two shapes a
 * hand-written layout could draw.
 *
 * The `diagram` JSON fence of older answers still draws. Every stored thread
 * holds one, and toMermaid (diagramExport.ts) already wrote it as mermaid for
 * the clipboard; the same conversion now feeds the renderer.
 */

// Every drawing's element ids start with rongo's name: two drawings on one
// page must not share one. Before the first fence, as the package asks.
configure({ ids: "rongo-diagram" });

// ---- legacy spec ----

export type FlowKind = "start" | "end" | "step" | "decision";
export type FlowNode = {
  id: string;
  label: string;
  kind: FlowKind;
};
type FlowEdge = { from: string; to: string; label?: string };
export type FlowSpec = { type: "flow"; nodes: FlowNode[]; edges: FlowEdge[] };
type Actor = { id: string; label: string };
type StepKind = "call" | "return" | "async";
type SeqStep = {
  from: string;
  to: string;
  label: string;
  kind: StepKind;
};
export type SequenceSpec = {
  type: "sequence";
  actors: Actor[];
  steps: SeqStep[];
};
export type DiagramSpec = FlowSpec | SequenceSpec;

const flowKinds: readonly string[] = ["start", "end", "step", "decision"];
const stepKinds: readonly string[] = ["call", "return", "async"];

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** parseDiagram reads the older JSON spec, or returns null when there is no
 * spec in the body at all. Every stored answer up to 2026-09 holds one.
 *
 * It degrades per element, never per diagram. Three times a picture has been
 * thrown away whole because one field did not match: too many actors (#60), a
 * src written as prose (#45), a fence the model tagged its own way. Each fix
 * hardened the one gate that had just failed, and the next deviation found
 * the next gate. So the rule here is that a spec is drawn as far as it can
 * be: an unknown kind falls back to the ordinary one, an edge naming an id
 * nobody declared is dropped, a node with no label is dropped, and what is
 * left is drawn. Only a body with no recognisable type, or one with nothing
 * left to draw after that, is null. Size is not a reason either. */
export function parseDiagram(body: string): DiagramSpec | null {
  let raw: unknown;
  try {
    raw = JSON.parse(body);
  } catch {
    return null;
  }
  if (!isRecord(raw)) return null;
  if (raw.type === "flow") {
    const nodes = Array.isArray(raw.nodes) ? raw.nodes.filter(isRecord) : [];
    const edges = Array.isArray(raw.edges) ? raw.edges.filter(isRecord) : [];
    const out: FlowNode[] = [];
    const known = new Set<string>();
    for (const n of nodes) {
      if (typeof n.id !== "string" || n.id === "" || known.has(n.id)) continue;
      if (typeof n.label !== "string") continue;
      const kind = flowKinds.includes(n.kind as string)
        ? (n.kind as FlowKind)
        : "step";
      known.add(n.id);
      out.push({ id: n.id, label: n.label, kind });
    }
    if (out.length === 0) return null;
    const es: FlowEdge[] = [];
    for (const e of edges) {
      if (typeof e.from !== "string" || typeof e.to !== "string") continue;
      if (!known.has(e.from) || !known.has(e.to)) continue;
      es.push({
        from: e.from,
        to: e.to,
        label: typeof e.label === "string" ? e.label : undefined,
      });
    }
    return { type: "flow", nodes: out, edges: es };
  }
  if (raw.type === "sequence") {
    const actors = Array.isArray(raw.actors) ? raw.actors.filter(isRecord) : [];
    const steps = Array.isArray(raw.steps) ? raw.steps.filter(isRecord) : [];
    const as: Actor[] = [];
    const known = new Set<string>();
    for (const a of actors) {
      if (typeof a.id !== "string" || a.id === "" || known.has(a.id)) continue;
      if (typeof a.label !== "string") continue;
      known.add(a.id);
      as.push({ id: a.id, label: a.label });
    }
    if (as.length === 0) return null;
    const ss: SeqStep[] = [];
    for (const s of steps) {
      if (
        typeof s.from !== "string" ||
        typeof s.to !== "string" ||
        typeof s.label !== "string"
      )
        continue;
      if (!known.has(s.from) || !known.has(s.to)) continue;
      const kind = stepKinds.includes(s.kind as string)
        ? (s.kind as StepKind)
        : "call";
      ss.push({ from: s.from, to: s.to, label: s.label, kind });
    }
    return { type: "sequence", actors: as, steps: ss };
  }
  return null;
}

// ---- source ----

/** diagramSource is the mermaid text a fence holds, or null when the fence
 * is not a diagram at all.
 *
 * A `mermaid` fence is its own source. A `diagram` fence is the older JSON
 * spec, converted; a `diagram` fence that is not JSON is taken as mermaid
 * text the model tagged its own way, because the tag says what it meant. Any
 * other tag counts only when its body opens as a spec: a `json` fence
 * quoting a config with "type": "flow" is code, and stays code, unless it
 * really is one of ours. A body that opens as a spec and does not parse is
 * handed on as it is, so the renderer refuses it and the card says so: for
 * three releases a broken spec looked like an ordinary code block. */
export function diagramSource(tag: string, body: string): string | null {
  if (specRe.test(body)) {
    const spec = parseDiagram(body);
    return spec ? toMermaid(spec) : body;
  }
  if (tag === "mermaid" || tag === "diagram") return body;
  return null;
}

/** specRe says a fence body opens as a legacy spec. Anchored on the brace, so
 * prose about the format inside a code block is not mistaken for one. */
const specRe = /^\s*\{\s*"type"\s*:\s*"(?:flow|sequence)"/;

// ---- the card ----

/** useDrawn is the drawing for a source, null while it is on its way. */
export function useDrawn(src: string): Drawn | null {
  return useResolved(src, draw);
}

const icons = { expand: <ExpandIcon />, download: <DownloadIcon /> };

/** Diagram is the picture as it sits in an answer: the package's card, with
 * its two ways out, the full view (in rongo's overlay) and the file.
 *
 * It owns its own fallbacks because the drawing is asynchronous. While the
 * renderer works the reader sees that a picture is on its way; a source the
 * renderer refuses is a defect, and for three releases it looked like an
 * ordinary code block. It says so now, and keeps the source underneath so the
 * next one can be diagnosed at a glance. */
export default function Diagram({ src }: { src: string }): ReactNode {
  const out = useDrawn(src);

  if (out === null) return <DiagramPending />;
  if ("error" in out) {
    return (
      <details className="mt-3 rounded-ui-sm border border-border bg-panel p-3 font-sans text-sm text-muted">
        <summary className="cursor-pointer">Diagram could not be drawn</summary>
        <pre className="mt-2 overflow-x-auto font-mono text-[13px] leading-relaxed">
          <code>{src}</code>
        </pre>
      </details>
    );
  }

  return (
    <DiagramCard
      svg={out.svg}
      // A real boolean: the card takes a missing `own` for the package's
      // drawing, and the renderer's would lose its frame.
      own={out.own === true}
      title={diagramTitle(src)}
      fileName={fileName(diagramKind(src), "rongo")}
      shell={OverlayShell}
      icons={icons}
    />
  );
}

/** The box a diagram's place holds while the picture is still being drawn,
 * or its fence still being streamed. */
export function DiagramPending() {
  return (
    <div className="mt-3 rounded-ui-sm border border-border bg-panel p-3 font-sans text-sm text-muted">
      Drawing the diagram…
    </div>
  );
}
