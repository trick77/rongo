import { diagramKind, parseDiagram, type DiagramSpec, type FlowKind } from "./diagram";
import { fenceRe } from "./markdown";

/**
 * Taking a diagram out of rongo: as a standalone .svg file, and as mermaid
 * inside the copied markdown.
 *
 * The file is the renderer's own SVG with a ground put under it. "Copy as
 * Markdown" copies the answer text; a `mermaid` fence in it already reads
 * everywhere, and the older `diagram` JSON fence is rewritten to one, since
 * that shape is rongo's own and draws nowhere else.
 */

// ---- SVG file ----

/** toSvgFile turns a rendered diagram into a file that stands on its own.
 * The renderer already inlines its stylesheet and a viewBox, so the string
 * lacks only its ground: on the page the dark panel is the card's, not the
 * SVG's, and in a file a viewer paints white behind it and the warm off-white
 * labels all but disappear. The ground is read off the card rather than
 * named here, so there is one copy of the token. */
export function toSvgFile(drawn: { svg: string }, card: Element | null): string {
  const ground = card ? groundOf(card) : null;
  if (ground === null) return drawn.svg;
  return withGround(drawn.svg, ground);
}

/** withGround paints a colour under the whole drawing, as its first child so
 * it sits behind everything else. */
export function withGround(svg: string, ground: string): string {
  return svg.replace(/<svg\b[^>]*>/, (open) => {
    // The ground covers the viewBox, not the user-space origin: the
    // renderer's viewBox starts left of and above 0 (a sequence at y -25,
    // a flowchart at minus its padding), and a rect at 0,0 would leave a
    // bare band along the top and left edge of the file.
    const vb = /viewBox="([^"]*)"/.exec(open);
    const [x, y, w, h] = vb ? vb[1].trim().split(/[\s,]+/) : ["0", "0", "100%", "100%"];
    return `${open}<rect x="${x}" y="${y}" width="${w}" height="${h}" fill="${ground}"/>`;
  });
}

/** groundOf is the first real background behind the drawing, walked up from
 * the card. */
function groundOf(el: Element): string | null {
  for (let node: Element | null = el; node; node = node.parentElement) {
    const bg = getComputedStyle(node).backgroundColor;
    if (bg !== "" && bg !== "transparent" && !/^rgba\(.*,\s*0\)$/.test(bg)) return bg;
  }
  return null;
}

/** download hands the browser a file: an SVG as its text, anything else as
 * the blob it already is. There is no shared helper in the UI to reuse: this
 * is the first thing rongo lets anyone take away. */
export function download(name: string, data: string | Blob): void {
  const blob = typeof data === "string" ? new Blob([data], { type: "image/svg+xml;charset=utf-8" }) : data;
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

/** fileName is what the download is called: the kind of picture it is, so a
 * folder of them stays sortable. */
export function fileName(src: string): string {
  const kind = diagramKind(src).replace(/Diagram(-v2)?$/, "").toLowerCase();
  if (kind === "") return "rongo-diagram.svg";
  return `rongo-${kind === "graph" ? "flowchart" : kind}-diagram.svg`;
}

// ---- PNG file ----

/** svgSize is the size a drawing asks for: the width and height its root
 * element states, else its viewBox. */
export function svgSize(svg: string): { w: number; h: number } | null {
  const open = /<svg\b[^>]*>/.exec(svg)?.[0] ?? "";
  const w = parseFloat(/\swidth="([\d.]+)(?:px)?"/.exec(open)?.[1] ?? "");
  const h = parseFloat(/\sheight="([\d.]+)(?:px)?"/.exec(open)?.[1] ?? "");
  if (w > 0 && h > 0) return { w, h };
  const vb = /viewBox="([^"]*)"/.exec(open)?.[1].trim().split(/[\s,]+/).map(Number);
  if (vb && vb.length === 4 && vb[2] > 0 && vb[3] > 0) return { w: vb[2], h: vb[3] };
  return null;
}

/** A canvas past these draws nothing and encodes to nothing, silently: 16384
 * is the side every current browser holds, and the area stays at half of
 * what that side allows. */
const canvasSide = 16384;
const canvasArea = 16384 * 8192;

/** pngScale is how much larger than 1:1 the PNG is drawn: twice, for a
 * picture that stays sharp on a dense screen, less where that would not fit
 * a canvas. A diagram too large even at 1:1 is drawn smaller rather than not
 * at all. */
export function pngScale(w: number, h: number): number {
  return Math.min(2, canvasSide / w, canvasSide / h, Math.sqrt(canvasArea / (w * h)));
}

/** toPng paints a drawing on white, the paper it was drawn for, and encodes
 * it. Through a data: URL, not a blob: URL, which some browsers count as
 * foreign content and then refuse to read back off the canvas. */
export function toPng(svg: string): Promise<Blob> {
  const size = svgSize(svg);
  if (size === null) return Promise.reject(new Error("the drawing states no size"));
  const scale = pngScale(size.w, size.h);
  const w = Math.max(1, Math.round(size.w * scale));
  const h = Math.max(1, Math.round(size.h * scale));
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => {
      const canvas = document.createElement("canvas");
      canvas.width = w;
      canvas.height = h;
      const ctx = canvas.getContext("2d");
      if (!ctx) return reject(new Error("the browser gave no canvas"));
      ctx.fillStyle = "#FFFFFF";
      ctx.fillRect(0, 0, w, h);
      ctx.drawImage(img, 0, 0, w, h);
      canvas.toBlob((b) => (b ? resolve(b) : reject(new Error("the picture is too large for a PNG"))), "image/png");
    };
    img.onerror = () => reject(new Error("the drawing could not be read"));
    img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
  });
}

// ---- mermaid ----

/** Words mermaid reads as syntax rather than as a name. `end` is the one that
 * matters: the flow spec has a node kind called "end", so `{"id":"end"}` is
 * exactly what a model writes — and `end` closes a block in sequenceDiagram
 * and is a parse error as a flowchart node, taking the whole picture down. */
const reserved = new Set([
  "end",
  "graph",
  "subgraph",
  "class",
  "classDef",
  "style",
  "click",
  "link",
  "linkStyle",
  "direction",
  "flowchart",
  "participant",
  "actor",
  "note",
  "loop",
  "alt",
  "else",
  "opt",
  "par",
  "rect",
  "activate",
  "deactivate",
]);

/** Ids are model output and may hold spaces, dots or dashes; a mermaid
 * identifier may not. Sanitizing can collide ("a.b" and "a-b" both become
 * "a_b"), so the map keeps them apart. */
function safeIds(ids: string[]): Map<string, string> {
  const out = new Map<string, string>();
  const taken = new Set<string>();
  for (const id of ids) {
    let base = id.replace(/[^A-Za-z0-9_]/g, "_") || "n";
    if (reserved.has(base)) base = `${base}_`;
    let name = base;
    for (let n = 2; taken.has(name); n++) name = `${base}_${n}`;
    taken.add(name);
    out.set(id, name);
  }
  return out;
}

/** A label goes inside quotes, so the only character that has to leave is the
 * quote itself — `[1]`, colons and parentheses all survive there. Newlines
 * would end the statement. */
function quoted(label: string): string {
  return `"${label.replace(/"/g, "#quot;").replace(/\s+/g, " ").trim()}"`;
}

/** A sequence message is everything after the colon to the end of the line,
 * so it needs no quotes — only the characters that would end it early. */
function bare(label: string): string {
  return label.replace(/[;#]/g, "").replace(/\s+/g, " ").trim();
}

/** cited renders a step's sources the way the prose writes them, so the
 * numbers in a pasted diagram still point into the `Sources:` block under it. */
function cited(src: number[]): string {
  return src.map((m) => ` [${m}]`).join("");
}

const arrows: Record<string, string> = { call: "->>", return: "-->>", async: "-)" };

function shape(kind: FlowKind, label: string): string {
  const l = quoted(label);
  if (kind === "start" || kind === "end") return `([${l}])`;
  if (kind === "decision") return `{${l}}`;
  return `[${l}]`;
}

/** toMermaid writes the spec as the diagram syntax GitHub, GitLab, Obsidian
 * and Notion draw, and that reads as plain text everywhere else.
 *
 * cite says whether a node's markers go into its label. On the clipboard
 * they do: the numbers point into the Sources block pasted under the text.
 * Drawn in the answer they do not: the picture no longer cites, and a spec
 * the backend never renumbered would draw the prompt's numbers as if they
 * were the reader's. */
export function toMermaid(spec: DiagramSpec, cite = true): string {
  const markers = cite ? cited : () => "";
  if (spec.type === "sequence") {
    const id = safeIds(spec.actors.map((a) => a.id));
    const out = ["sequenceDiagram"];
    // An empty label is a spec parseDiagram accepts, and `participant x as `
    // with nothing after it takes the whole diagram down with a parse error.
    for (const a of spec.actors) out.push(`    participant ${id.get(a.id)} as ${bare(a.label) || a.id}`);
    for (const s of spec.steps) {
      const arrow = arrows[s.kind] ?? arrows.call;
      out.push(`    ${id.get(s.from)}${arrow}${id.get(s.to)}: ${bare(s.label)}${markers(s.src)}`);
    }
    return out.join("\n");
  }
  const id = safeIds(spec.nodes.map((n) => n.id));
  const out = ["flowchart TD"];
  for (const n of spec.nodes) out.push(`    ${id.get(n.id)}${shape(n.kind, n.label + markers(n.src))}`);
  for (const e of spec.edges) {
    const link = e.label ? `-->|${quoted(e.label)}|` : "-->";
    out.push(`    ${id.get(e.from)} ${link} ${id.get(e.to)}`);
  }
  return out.join("\n");
}

/** mermaidize rewrites the older diagram fences in an answer for the
 * clipboard: a JSON spec becomes mermaid text, and a `diagram` tag over
 * mermaid text (which diagram.tsx draws) becomes the `mermaid` tag every
 * other renderer knows. */
export function mermaidize(text: string): string {
  const lines = text.split("\n");
  const out: string[] = [];
  let i = 0;
  while (i < lines.length) {
    const fence = fenceRe.exec(lines[i]);
    if (!fence || fence[1] !== "diagram") {
      out.push(lines[i++]);
      continue;
    }
    const open = lines[i++];
    const body: string[] = [];
    while (i < lines.length && !fenceRe.test(lines[i])) body.push(lines[i++]);
    const closed = i < lines.length;
    if (closed) i++;
    if (!closed) {
      out.push(open, ...body);
      continue;
    }
    const spec = parseDiagram(body.join("\n"));
    out.push("```mermaid", ...(spec ? toMermaid(spec).split("\n") : body), "```");
  }
  return out.join("\n");
}
