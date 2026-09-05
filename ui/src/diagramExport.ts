import { parseDiagram, type DiagramSpec, type FlowKind } from "./diagram";
import { fenceRe } from "./markdown";

/**
 * Taking a diagram out of rongo: as a standalone .svg file, and as mermaid
 * inside the copied markdown.
 *
 * Both exist because the drawing in the answer is only readable inside the
 * app. The SVG on the page carries no colours of its own — every fill and
 * stroke is a Tailwind utility bound to a theme token — so a serialized node
 * lands in a viewer as flat black on white. And "Copy as Markdown" copies the
 * answer text, which holds the diagram as its `diagram` JSON fence: correct,
 * and unreadable everywhere outside rongo.
 */

// ---- SVG file ----

/** The properties a shape in diagram.tsx gets from a class rather than an
 * attribute. Read off the live node, so the file follows the @theme tokens
 * instead of a second copy of them kept here. */
const inlined = [
  "fill",
  "stroke",
  "stroke-width",
  "stroke-dasharray",
  "font-family",
  "font-size",
  "font-weight",
] as const;

/** Elements that carry no paint of their own. */
const skipPaint = new Set(["svg", "title", "desc", "defs", "marker"]);

/** toSvgFile turns a drawn diagram into a file that stands on its own: the
 * colours inlined, the classes dropped, and a viewBox added. The viewBox is
 * deliberate here and deliberately absent on the page — a file is opened in a
 * viewer that scales it to the window, while the element in the answer must
 * keep its intrinsic width and scroll (diagram.tsx). */
export function toSvgFile(el: SVGSVGElement): string {
  const clone = el.cloneNode(true) as SVGSVGElement;
  const live = [el, ...el.querySelectorAll("*")];
  const copy = [clone, ...clone.querySelectorAll("*")];
  for (let i = 0; i < live.length && i < copy.length; i++) {
    // Only what draws. Painting the root, the <title> or a <defs> wrapper
    // would put a colour on elements that have none and leave the file
    // carrying a paragraph of inherited defaults per node.
    if (skipPaint.has(copy[i].tagName)) continue;
    const style = getComputedStyle(live[i] as Element);
    for (const prop of inlined) {
      const v = style.getPropertyValue(prop);
      if (v !== "") copy[i].setAttribute(prop, v);
    }
  }
  // The chips' hit rects are drawn only because an SVG group cannot take
  // padding; in a file they are invisible rectangles over the drawing.
  for (const hit of clone.querySelectorAll('[data-export="skip"]')) hit.remove();
  for (const node of copy) {
    node.removeAttribute("class");
    node.removeAttribute("role");
    node.removeAttribute("tabindex");
    for (const a of [...node.attributes]) {
      if (a.name.startsWith("aria-") || a.name.startsWith("data-")) node.removeAttribute(a.name);
    }
  }
  const w = el.getAttribute("width") ?? "0";
  const h = el.getAttribute("height") ?? "0";
  // The drawing's ground travels with it. On the page the dark panel is the
  // card's, not the SVG's; in a file, a viewer paints white behind it and the
  // warm off-white labels all but disappear.
  const ground = groundOf(el);
  if (ground !== null) {
    const rect = clone.ownerDocument.createElementNS("http://www.w3.org/2000/svg", "rect");
    rect.setAttribute("width", w);
    rect.setAttribute("height", h);
    rect.setAttribute("fill", ground);
    clone.insertBefore(rect, clone.firstChild);
  }
  clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
  clone.setAttribute("viewBox", `0 0 ${w} ${h}`);
  return new XMLSerializer().serializeToString(clone);
}

/** groundOf is the first real background behind the drawing. Walked rather
 * than named, for the same reason the paint is read and not tabulated: the
 * colour is a theme token, and there should be one copy of it. */
function groundOf(el: Element): string | null {
  for (let node: Element | null = el; node; node = node.parentElement) {
    const bg = getComputedStyle(node).backgroundColor;
    if (bg !== "" && bg !== "transparent" && !/^rgba\(.*,\s*0\)$/.test(bg)) return bg;
  }
  return null;
}

/** download hands the browser a file. There is no shared helper in the UI to
 * reuse: this is the first thing rongo lets anyone take away. */
export function download(name: string, svg: string): void {
  const url = URL.createObjectURL(new Blob([svg], { type: "image/svg+xml;charset=utf-8" }));
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
export function fileName(spec: DiagramSpec): string {
  return spec.type === "flow" ? "rongo-flow-diagram.svg" : "rongo-sequence-diagram.svg";
}

// ---- mermaid ----

/** Ids are model output and may hold spaces, dots or dashes; a mermaid
 * identifier may not. Sanitizing can collide ("a.b" and "a-b" both become
 * "a_b"), so the map keeps them apart. */
function safeIds(ids: string[]): Map<string, string> {
  const out = new Map<string, string>();
  const taken = new Set<string>();
  for (const id of ids) {
    const base = id.replace(/[^A-Za-z0-9_]/g, "_") || "n";
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

/** markers renders a step's sources the way the prose writes them, so the
 * numbers in a pasted diagram still point into the `Sources:` block under it. */
function markers(src: number[]): string {
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
 * and Notion draw, and that reads as plain text everywhere else. */
export function toMermaid(spec: DiagramSpec): string {
  if (spec.type === "sequence") {
    const id = safeIds(spec.actors.map((a) => a.id));
    const out = ["sequenceDiagram"];
    for (const a of spec.actors) out.push(`    participant ${id.get(a.id)} as ${bare(a.label)}`);
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

/** mermaidize rewrites the diagram fences in an answer for the clipboard. A
 * fence that does not parse is left exactly as it is: it is not a diagram
 * this renderer drew either, and the reader gets what the answer said. */
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
    const close = closed ? lines[i++] : null;
    const spec = closed ? parseDiagram(body.join("\n")) : null;
    if (!spec) {
      out.push(open, ...body);
      if (close !== null) out.push(close);
      continue;
    }
    out.push("```mermaid", ...toMermaid(spec).split("\n"), "```");
  }
  return out.join("\n");
}
