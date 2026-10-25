import { diagramKind } from "./diagram";

/**
 * Angle brackets in a diagram's text, kept as text.
 *
 * mermaid's sanitiser takes anything in angle brackets for a tag and drops
 * it, in rongo's drawing and its own alike: a message written `Failed to
 * fetch <url>: <cause>` drew as `Failed to fetch :`, a guard `retries < 3`
 * lost its tail. There is no switch in the renderer that keeps them, so on
 * the way to drawing every `<` and `>` in the text is written as the
 * renderer's entity code, which it turns back into the character.
 *
 * "The text" is found by grammar, not by a parser: quoted strings anywhere,
 * a flowchart's `|edge label|`, what follows a sequence line's keyword or a
 * message's colon, a state, class or entity line's text after its colon,
 * the body of a state note. Arrows (`-->`, `->>`) and the formatting tags the
 * renderer honours (`<br>`, `<b>`, `<code>` ...) are never touched. A form
 * this does not know is left as it was, never broken: draw() falls back to
 * the source as written whenever the escaped one fails to parse.
 *
 * Applied on the way to drawing only; the stored fence and what Copy as
 * Markdown copies stay as the model wrote them.
 */

/** Tags the renderer treats as formatting, kept as markup. */
const markup =
  /^<\/?(?:br|b|i|u|em|strong|code|sub|sup|small|s|del|ins|mark|span)\b[^<>]*>/i;

/** esc writes the brackets of one piece of text as entity codes, leaving the
 * formatting tags in it alone. */
function esc(s: string): string {
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

/** quoted escapes every double-quoted string, however many lines it runs
 * over: a label's closing quote is found by scanning, never by pairing the
 * quotes of one line, which paired a label's end with the next one's start
 * and escaped the arrow between them. */
function quoted(src: string): string {
  let out = "";
  let i = 0;
  while (i < src.length) {
    const open = src.indexOf('"', i);
    if (open < 0) break;
    const close = src.indexOf('"', open + 1);
    if (close < 0) break;
    out += src.slice(i, open + 1) + esc(src.slice(open + 1, close)) + '"';
    i = close + 1;
  }
  return out + src.slice(i);
}

/** The words that open a sequence line whose remainder is text. */
const sequenceWords =
  /^(\s*)(alt|else|opt|loop|par|and|critical|option|break|rect|box|participant|actor|create participant|create actor|note|title)\b(.*)$/i;

/** afterColon escapes what follows a line's first colon. */
function afterColon(line: string): string {
  const at = line.indexOf(":");
  return at < 0 ? line : line.slice(0, at + 1) + esc(line.slice(at + 1));
}

export function escapeAngles(src: string): string {
  if (!/[<>]/.test(src)) return src;
  const kind = diagramKind(src);
  const text = quoted(src);
  const lines = text.split("\n");
  let inNote = false;
  return lines
    .map((line) => {
      const t = line.trim();
      if (t.startsWith("%%")) return line;
      if (kind === "flowchart" || kind === "graph") {
        // Edge labels between pipes; a quoted one is already done.
        return line.replace(/\|([^|"]*)\|/g, (_, s: string) => `|${esc(s)}|`);
      }
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
        if (/^note\b/i.test(t) && !t.includes(":")) {
          inNote = true;
          return line;
        }
        return afterColon(line);
      }
      if (kind === "classDiagram" || kind === "erDiagram") {
        return afterColon(line);
      }
      return line;
    })
    .join("\n");
}
