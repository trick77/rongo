import { diagramKind } from "./diagramKind";

/**
 * Angle brackets in a diagram's text, kept as text.
 *
 * mermaid's sanitiser takes anything in angle brackets for a tag and drops
 * it, in rongo's drawing and its own alike: a message written `Failed to
 * fetch <url>: <cause>` drew as `Failed to fetch :`, a guard `retries < 3`
 * lost its tail, `Repository<S>` lost its parameter. There is no switch in
 * the renderer that keeps them, so on the way to drawing every `<` and `>`
 * in the text is written as the renderer's entity code, which it turns back
 * into the character.
 *
 * "The text" is found by grammar, not by a parser: quoted strings, and per
 * kind the unquoted forms the model writes. A flowchart's node labels,
 * `|edge labels|`, `-- edge labels -->` and subgraph titles; what follows a
 * sequence line's keyword or a message's colon; a state, class or entity
 * line's text after its colon, and a state note's body. Arrows (`-->`,
 * `->>`), `:::class` shorthands and the formatting tags the renderer honours
 * (`<br>`, `<b>`, `<code>` ..., lowercase and exact) are never touched. A form
 * this does not know is left as it was: draw() falls back to the source as
 * written, and says so, whenever the escaped one fails to parse.
 *
 * Applied on the way to drawing only; the stored fence and what Copy as
 * Markdown copies stay as the model wrote them.
 */

/** A formatting tag the renderer honours, kept as markup: lowercase, the
 * name alone or followed by attributes after a space. `<S>`, `<I, V>`,
 * `<Code>` are type parameters, not tags. */
const markup =
  /^<\/?(?:br|b|i|u|em|strong|code|sub|sup|small|s|del|ins|mark|span)(?:\s[^<>]*)?\/?>/;

/** esc writes the brackets of one piece of text as entity codes, leaving the
 * formatting tags in it alone. */
export function esc(s: string): string {
  let out = "";
  for (let i = 0; i < s.length;) {
    if (s[i] === "<") {
      const m = markup.exec(s.slice(i));
      if (m) {
        out += m[0];
        i += m[0].length;
        continue;
      }
      out += "#lt;";
    } else if (s[i] === ">") {
      out += "#gt;";
    } else {
      out += s[i];
    }
    i++;
  }
  return out;
}

/** quotedIn escapes every double-quoted string of one piece of source. */
function quotedIn(src: string): string {
  return src.replace(/"([^"]*)"/g, (_, s: string) => `"${esc(s)}"`);
}

/** quotedAcross escapes the double-quoted strings of a flowchart, whose
 * markdown labels may run over several lines: a label's closing quote is
 * found by scanning, never by pairing the quotes of one line, which paired
 * a label's end with the next one's start and escaped the arrow between.
 * Comment lines are passed over: a quote there is free text. */
function quotedAcross(src: string): string {
  const lines = src.split("\n");
  const code = lines.map((l) => (l.trim().startsWith("%%") ? "" : l));
  const joined = code.join("\n");
  let out = "";
  let i = 0;
  while (i < joined.length) {
    const open = joined.indexOf('"', i);
    if (open < 0) break;
    const close = joined.indexOf('"', open + 1);
    if (close < 0) break;
    out += joined.slice(i, open + 1) + esc(joined.slice(open + 1, close)) + '"';
    i = close + 1;
  }
  const done = (out + joined.slice(i)).split("\n");
  return done.map((l, k) => (code[k] === "" ? lines[k] : l)).join("\n");
}

/** afterColon escapes what follows a line's first colon that is a colon of
 * its own, not part of a `:::class` shorthand. */
function afterColon(line: string): string {
  const m = /(?<!:):(?!:)/.exec(line);
  if (!m) return line;
  const at = m.index;
  return line.slice(0, at + 1) + esc(line.slice(at + 1));
}

/** Shapes a flowchart node's label sits in, outermost delimiters first. */
const shapes: [string, string][] = [
  ["([", "])"],
  ["[[", "]]"],
  ["[(", ")]"],
  ["((", "))"],
  ["{{", "}}"],
  ["[/", "/]"],
  ["[\\", "\\]"],
  ["[", "]"],
  ["(", ")"],
  ["{", "}"],
];

/** flowchartLine escapes the unquoted text of one flowchart line: node
 * labels inside their shape, `|edge labels|`, `-- edge labels -->`, and a
 * subgraph's title. Quoted text is already done and is passed over. */
function flowchartLine(line: string): string {
  const t = line.trimStart();
  if (/^subgraph\b/.test(t)) {
    const lead = line.length - t.length;
    const m = /^subgraph\s+(\S+)(.*)$/.exec(t);
    if (!m) return line;
    return line.slice(0, lead) + `subgraph ${m[1]}` + esc(m[2]);
  }
  let out = line.replace(/\|([^|"]*)\|/g, (_, s: string) => `|${esc(s)}|`);
  // `A -- text --> B`, `A == text ==> B`, `A -. text .-> B`.
  out = out.replace(
    /(\s(?:--|==|-\.)\s+)([^"|]*?)(\s+(?:-->|==>|\.->|---|-\.-))/g,
    (_, a: string, s: string, b: string) => a + esc(s) + b,
  );
  // An unquoted label inside its shape, right after a node id.
  let res = "";
  let i = 0;
  const id = /[A-Za-z0-9_][\w-]*/y;
  while (i < out.length) {
    id.lastIndex = i;
    const m = id.exec(out);
    if (!m) {
      res += out[i++];
      continue;
    }
    let j = i + m[0].length;
    res += m[0];
    const shape = shapes.find(([o]) => out.startsWith(o, j));
    if (!shape || out[j + shape[0].length] === '"') {
      i = j;
      continue;
    }
    const [o, c] = shape;
    const end = out.indexOf(c, j + o.length);
    if (end < 0) {
      i = j;
      continue;
    }
    res += o + esc(out.slice(j + o.length, end)) + c;
    i = end + c.length;
    j = i;
  }
  return res;
}

/** The words that open a sequence line whose remainder is text. */
const sequenceWords =
  /^(\s*)(alt|else|opt|loop|par|and|critical|option|break|rect|box|participant|actor|create participant|create actor|note|title)\b(.*)$/i;

export function escapeAngles(src: string): string {
  if (!/[<>]/.test(src)) return src;
  const kind = diagramKind(src);
  const flow = kind === "graph" || kind.startsWith("flowchart");
  const text = flow ? quotedAcross(src) : src;
  let inNote = false;
  return text
    .split("\n")
    .map((raw) => {
      const t = raw.trim();
      if (t.startsWith("%%")) return raw;
      if (flow) return flowchartLine(raw);
      const line = quotedIn(raw);
      if (kind === "sequenceDiagram") {
        const w = sequenceWords.exec(line);
        if (w) return w[1] + w[2] + esc(w[3]);
        return afterColon(line);
      }
      if (kind.startsWith("stateDiagram")) {
        if (inNote) {
          if (/^end note\b/i.test(t)) inNote = false;
          else return esc(line);
          return line;
        }
        if (/^note\b/i.test(t) && !/(?<!:):(?!:)/.test(t)) {
          inNote = true;
          return line;
        }
        return afterColon(line);
      }
      if (kind.startsWith("classDiagram") || kind === "erDiagram") {
        return afterColon(line);
      }
      return line;
    })
    .join("\n");
}
