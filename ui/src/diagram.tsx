import { useEffect, useRef, useState, type ReactNode } from "react";
import { download, fileName, toMermaid, toSvgFile } from "./diagramExport";
import DiagramView from "./DiagramView";
import { DownloadIcon, ExpandIcon } from "./icons";

/**
 * One diagram in an answer: a fenced block tagged `mermaid`, drawn by the
 * mermaid renderer into an SVG string that is then set as innerHTML.
 *
 * That is model output reaching innerHTML, which markdown.tsx refuses on
 * principle, and the reason it is allowed here is the renderer's own guard:
 * `securityLevel: "strict"` runs every label through DOMPurify and forbids
 * HTML labels, links and click bindings, so a prompt injection ends as text
 * in a box. Nothing calls bindFunctions, so nothing the spec says is ever
 * executed.
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
 *
 * The renderer measures text with getBBox, which jsdom lacks, so a test
 * mocks `mermaid` and asserts on what this file does around it: parse,
 * cache, fall back. mermaid.parse alone runs under jsdom, and corpus.test.ts
 * uses it to prove every corpus answer is drawable.
 */

// ---- legacy spec ----

export type FlowKind = "start" | "end" | "step" | "decision";
export type FlowNode = { id: string; label: string; kind: FlowKind; src: number[] };
export type FlowEdge = { from: string; to: string; label?: string };
export type FlowSpec = { type: "flow"; nodes: FlowNode[]; edges: FlowEdge[] };
export type Actor = { id: string; label: string };
export type StepKind = "call" | "return" | "async";
export type SeqStep = { from: string; to: string; label: string; kind: StepKind; src: number[] };
export type SequenceSpec = { type: "sequence"; actors: Actor[]; steps: SeqStep[] };
export type DiagramSpec = FlowSpec | SequenceSpec;

const flowKinds: readonly string[] = ["start", "end", "step", "decision"];
const stepKinds: readonly string[] = ["call", "return", "async"];

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** src keeps a node's markers only when every one of them is a reader's
 * number, all or nothing: the backend of the time renumbered a src array all
 * or nothing, so one quoted or fractional entry left the whole array at the
 * prompt's indices. They reach the clipboard (toMermaid, cite), never the
 * picture. */
function src(v: unknown): number[] {
  if (!Array.isArray(v)) return [];
  if (!v.every((n) => Number.isInteger(n) && (n as number) > 0)) return [];
  return v as number[];
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
      const kind = flowKinds.includes(n.kind as string) ? (n.kind as FlowKind) : "step";
      known.add(n.id);
      out.push({ id: n.id, label: n.label, kind, src: src(n.src) });
    }
    if (out.length === 0) return null;
    const es: FlowEdge[] = [];
    for (const e of edges) {
      if (typeof e.from !== "string" || typeof e.to !== "string") continue;
      if (!known.has(e.from) || !known.has(e.to)) continue;
      es.push({ from: e.from, to: e.to, label: typeof e.label === "string" ? e.label : undefined });
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
      if (typeof s.from !== "string" || typeof s.to !== "string" || typeof s.label !== "string") continue;
      if (!known.has(s.from) || !known.has(s.to)) continue;
      const kind = stepKinds.includes(s.kind as string) ? (s.kind as StepKind) : "call";
      ss.push({ from: s.from, to: s.to, label: s.label, kind, src: src(s.src) });
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
    return spec ? toMermaid(spec, false) : body;
  }
  if (tag === "mermaid" || tag === "diagram") return body;
  return null;
}

/** specRe says a fence body opens as a legacy spec. Anchored on the brace, so
 * prose about the format inside a code block is not mistaken for one. */
export const specRe = /^\s*\{\s*"type"\s*:\s*"(?:flow|sequence)"/;

/** diagramKind is the first word of the source, which is the diagram type in
 * mermaid's grammar: `flowchart`, `sequenceDiagram`, `stateDiagram-v2`. A
 * leading `%%{init}%%` directive or comment is skipped. Empty when there is
 * no such word, or when the word is not the shape of a type name; the
 * backend reads it the same way (renumber.go mermaidKind). */
export function diagramKind(src: string): string {
  for (const line of src.split("\n")) {
    const t = line.trim();
    if (t === "" || t.startsWith("%%")) continue;
    const word = t.split(/\s+/)[0].replace(/[;:]$/, "");
    return /^[A-Za-z][A-Za-z0-9-]*$/.test(word) ? word : "";
  }
  return "";
}

const titles: Record<string, string> = {
  flowchart: "Flow diagram",
  graph: "Flow diagram",
  sequenceDiagram: "Sequence diagram",
  stateDiagram: "State diagram",
  "stateDiagram-v2": "State diagram",
  erDiagram: "Entity diagram",
  classDiagram: "Class diagram",
};

/** diagramTitle names the picture, in the accessible label, the file and the
 * full view's header. */
export function diagramTitle(src: string): string {
  return titles[diagramKind(src)] ?? "Diagram";
}

// ---- rendering ----

/** The theme is read off the document, not tabulated here: index.css says
 * nothing reaches for var(--color-…) directly, and this is the one exception
 * because the renderer wants hex values in a config object, not classes on
 * elements. Missing under jsdom, where the tokens are not loaded; the renderer
 * then keeps its own defaults.
 *
 * Every frame is `--color-faint`, and that is a measurement, not a taste: the
 * border token is chrome, where it separates a panel from the page ground, and
 * against a node fill four points away from it a frame drew at 1.3:1 and did
 * not appear on screen at all. Faint reads at 4:1 and stays under the arrows'
 * muted, so the flow remains the loudest thing drawn. A cluster sits on the
 * page ground instead and reads 3.6:1 there; nothing sits between the two, so
 * it takes the same value rather than a quieter rung that would draw nothing.
 * A test asserts the five border keys, but only the screen proves a line: a
 * declared colour and the painted pixel have disagreed before. */
export function themeVariables(): Record<string, string> {
  if (typeof getComputedStyle !== "function") return {};
  const style = getComputedStyle(document.documentElement);
  const token = (name: string) => style.getPropertyValue(name).trim();
  const vars: Record<string, string> = {};
  const set = (key: string, name: string) => {
    const v = token(name);
    if (v !== "") vars[key] = v;
  };
  set("background", "--color-bg");
  set("primaryColor", "--color-panel");
  set("primaryBorderColor", "--color-faint");
  set("primaryTextColor", "--color-ink");
  set("textColor", "--color-ink");
  set("lineColor", "--color-muted");
  set("signalColor", "--color-muted");
  set("signalTextColor", "--color-ink");
  set("secondaryColor", "--color-active");
  set("secondaryBorderColor", "--color-faint");
  set("secondaryTextColor", "--color-ink");
  set("tertiaryColor", "--color-ochre-wash");
  set("tertiaryBorderColor", "--color-ochre");
  set("tertiaryTextColor", "--color-ink");
  set("noteBkgColor", "--color-ochre-wash");
  set("noteBorderColor", "--color-ochre");
  set("noteTextColor", "--color-ink");
  set("actorBkg", "--color-active");
  set("actorBorder", "--color-faint");
  set("actorTextColor", "--color-ink");
  set("actorLineColor", "--color-faint");
  set("labelBoxBkgColor", "--color-panel");
  set("labelBoxBorderColor", "--color-faint");
  set("labelTextColor", "--color-ink");
  set("loopTextColor", "--color-ink");
  set("edgeLabelBackground", "--color-panel");
  set("clusterBkg", "--color-bg");
  set("clusterBorder", "--color-faint");
  set("titleColor", "--color-ink");
  set("attributeBackgroundColorOdd", "--color-panel");
  set("attributeBackgroundColorEven", "--color-active");
  set("fontFamily", "--font-sans");
  // The renderer's own default is 16px, which is prose size; a label is
  // chrome and reads at the size the rest of the chrome does.
  vars.fontSize = "13.5px";
  return vars;
}

/** The renderer is loaded on the first fence, not with the shell: it is the
 * largest dependency by far, and a page that draws no diagram — the share
 * page, the Repos page, most threads — should not carry it. One import
 * promise, so two fences arriving together load it once. */
type Mermaid = typeof import("mermaid").default;
let loading: Promise<Mermaid> | null = null;
function renderer(): Promise<Mermaid> {
  if (!loading) {
    loading = import("mermaid").then(
      (m) => {
        init(m.default);
        return m.default;
      },
      (e: unknown) => {
        // A load that failed (a redeploy took the chunk away, a blip) is
        // tried again by the next fence, not remembered for the session.
        loading = null;
        throw e;
      },
    );
  }
  return loading;
}

function init(mermaid: Mermaid): void {
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    theme: "base",
    // The renderer's default look ("neo") puts a drop shadow under every
    // box; the chrome has none, so the drawing has none either.
    look: "classic",
    themeVariables: themeVariables(),
    fontFamily: themeVariables().fontFamily,
    flowchart: { htmlLabels: false, curve: "basis" },
    sequence: { useMaxWidth: true },
    er: { useMaxWidth: true },
  });
}

export type Drawn = { svg: string } | { error: string };

/** One drawing per source text, however many times the answer re-renders.
 * The stream re-renders on every token, and a closed fence is drawn once.
 * Never evicted: a session's diagrams are a few strings, and the same
 * string mounted twice (the card and the full view) is one drawing. */
const drawn = new Map<string, Promise<Drawn>>();
let seq = 0;

/** draw renders a source to an SVG string, or to the parser's complaint.
 *
 * Only a drawing is kept. The renderer loads each diagram type as its own
 * chunk, so the first sequence or entity diagram of a session can fail for
 * a reason that is not the source (a redeploy took the hashed chunk away, a
 * network blip); kept, that failure would follow the source for the rest of
 * the session. A refused source is cheap to refuse again.
 *
 * Exported for the tests, which mock the renderer and assert on the cache. */
export function draw(src: string): Promise<Drawn> {
  let p = drawn.get(src);
  if (!p) {
    p = (async (): Promise<Drawn> => {
      try {
        const mermaid = await renderer();
        await mermaid.parse(src);
        const { svg } = await mermaid.render(`rongo-diagram-${++seq}`, src);
        return { svg };
      } catch (e) {
        drawn.delete(src);
        return { error: e instanceof Error ? e.message : String(e) };
      }
    })();
    drawn.set(src, p);
  }
  return p;
}

/** useDrawn is the drawing for a source, null while it is on its way. */
export function useDrawn(src: string): Drawn | null {
  const [out, setOut] = useState<Drawn | null>(null);
  useEffect(() => {
    let live = true;
    setOut(null);
    draw(src).then((d) => {
      if (live) setOut(d);
    });
    return () => {
      live = false;
    };
  }, [src]);
  return out;
}

/** MermaidSvg is the drawing itself, sized by the renderer: the SVG carries
 * a viewBox and a max-width of its own intrinsic width, so it fits a narrow
 * column by scaling down and never grows past 1:1. */
export function MermaidSvg({ svg, title }: { svg: string; title: string }): ReactNode {
  return (
    // font-sans explicitly: the diagram sits inside the answer's .ui-markdown
    // wrapper, which is serif prose. A node label is a name from the code,
    // not prose, and it reads as the rest of the chrome does.
    <div
      className="rongo-diagram font-sans"
      role="img"
      aria-label={title}
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}

/** Diagram is the picture as it sits in an answer: the card, and the two
 * ways out of it, the full view and the file.
 *
 * It owns its own fallbacks because the drawing is asynchronous. While the
 * renderer works the reader sees that a picture is on its way; a source the
 * renderer refuses is a defect, and for three releases it looked like an
 * ordinary code block. It says so now, and keeps the source underneath so the
 * next one can be diagnosed at a glance. */
export default function Diagram({ src }: { src: string }): ReactNode {
  const out = useDrawn(src);
  const [full, setFull] = useState(false);
  const card = useRef<HTMLDivElement>(null);
  const title = diagramTitle(src);

  if (out === null) {
    return (
      <div className="mt-3 rounded-ui-sm border border-border bg-panel p-3 font-sans text-sm text-muted">
        Drawing the diagram…
      </div>
    );
  }
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

  function save() {
    download(fileName(src), toSvgFile(out as { svg: string }, card.current));
  }

  return (
    <div ref={card} className="relative mt-3 rounded-ui-sm border border-border bg-panel p-3">
      {/* Always drawn, never revealed on hover: a phone has no hover, and the
          full view is the only way it can see a wide diagram whole. */}
      <div className="absolute top-1.5 right-1.5 z-10 flex items-center gap-0.5 bg-gradient-to-l from-panel from-70% to-transparent pl-6">
        <button
          type="button"
          onClick={() => setFull(true)}
          title="Full view"
          aria-label={`${title}: full view`}
          className="grid h-7 w-7 place-items-center rounded-ui-sm text-faint hover:bg-active hover:text-ink-dim"
        >
          <ExpandIcon />
        </button>
        <button
          type="button"
          onClick={save}
          title="Download SVG"
          aria-label={`${title}: download SVG`}
          className="grid h-7 w-7 place-items-center rounded-ui-sm text-faint hover:bg-active hover:text-ink-dim"
        >
          <DownloadIcon />
        </button>
      </div>
      <MermaidSvg svg={out.svg} title={title} />
      {full && <DiagramView src={src} svg={out.svg} onClose={() => setFull(false)} />}
    </div>
  );
}
