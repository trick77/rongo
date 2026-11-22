import { dropStrayMarks } from "@trick77/diagram/mermaid";
import { parseDiagram, type DiagramSpec, type FlowKind } from "./diagram";
import { fenceRe } from "./markdown";

/**
 * Taking a diagram out of rongo as mermaid inside the copied markdown. The
 * .svg file is the package's (@trick77/diagram export).
 *
 * "Copy as Markdown" copies the answer text; a `mermaid` fence in it already
 * reads everywhere, and the older `diagram` JSON fence is rewritten to one,
 * since that shape is rongo's own and draws nowhere else.
 */

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

const arrows: Record<string, string> = {
  call: "->>",
  return: "-->>",
  async: "-)",
};

function shape(kind: FlowKind, label: string): string {
  const l = quoted(label);
  if (kind === "start" || kind === "end") return `([${l}])`;
  if (kind === "decision") return `{${l}}`;
  return `[${l}]`;
}

/** toMermaid writes the spec as the diagram syntax GitHub, GitLab, Obsidian
 * and Notion draw, and that reads as plain text everywhere else.
 *
 * A node's markers never go into its label. Drawn, the picture does not
 * cite, and a spec the backend never renumbered would draw the prompt's
 * numbers as if they were the reader's; copied, an answer carries no
 * markers at all. */
export function toMermaid(spec: DiagramSpec): string {
  if (spec.type === "sequence") {
    const id = safeIds(spec.actors.map((a) => a.id));
    const out = ["sequenceDiagram"];
    // An empty label is a spec parseDiagram accepts, and `participant x as `
    // with nothing after it takes the whole diagram down with a parse error.
    for (const a of spec.actors)
      out.push(`    participant ${id.get(a.id)} as ${bare(a.label) || a.id}`);
    for (const s of spec.steps) {
      const arrow = arrows[s.kind] ?? arrows.call;
      out.push(
        `    ${id.get(s.from)}${arrow}${id.get(s.to)}: ${bare(s.label)}`,
      );
    }
    return out.join("\n");
  }
  const id = safeIds(spec.nodes.map((n) => n.id));
  const out = ["flowchart TD"];
  for (const n of spec.nodes)
    out.push(`    ${id.get(n.id)}${shape(n.kind, n.label)}`);
  for (const e of spec.edges) {
    const link = e.label ? `-->|${quoted(e.label)}|` : "-->";
    out.push(`    ${id.get(e.from)} ${link} ${id.get(e.to)}`);
  }
  return out.join("\n");
}

/** mermaidize rewrites the diagram fences in an answer for the clipboard:
 * a JSON spec becomes mermaid text, a `diagram` tag over mermaid text
 * (which diagram.tsx draws) becomes the `mermaid` tag every other renderer
 * knows, and a stray class mark is dropped as draw() drops it
 * (dropStrayMarks): another renderer refuses the whole fence over it. */
export function mermaidize(text: string): string {
  const lines = text.split("\n");
  const out: string[] = [];
  let i = 0;
  while (i < lines.length) {
    const fence = fenceRe.exec(lines[i]);
    if (!fence || (fence[1] !== "diagram" && fence[1] !== "mermaid")) {
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
    if (fence[1] === "mermaid") {
      out.push(open, ...dropStrayMarks(body.join("\n")).split("\n"), "```");
      continue;
    }
    const spec = parseDiagram(body.join("\n"));
    out.push(
      "```mermaid",
      ...(spec
        ? toMermaid(spec).split("\n")
        : dropStrayMarks(body.join("\n")).split("\n")),
      "```",
    );
  }
  return out.join("\n");
}
