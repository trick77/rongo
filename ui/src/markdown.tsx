import type { JSX, ReactNode } from "react";

/**
 * A small Markdown renderer covering exactly what the answer prompt produces:
 * headings, paragraphs, bold, inline code, fenced code and lists.
 *
 * It builds React nodes and never HTML. The text is model output, and
 * dangerouslySetInnerHTML would turn a prompt injection into a script tag.
 *
 * There is deliberately NO link or bracket syntax. Citation markers like [1]
 * are what makes an answer checkable; a renderer that consumed one — link
 * parsing, "tidy up stray brackets" — would break the evidence trail while
 * looking like it worked.
 *
 * Unterminated markup renders as text rather than swallowing the rest. The
 * answer is re-rendered on every streamed token, so half-written markup is the
 * normal case here, not an edge case.
 */

/** inline renders bold and inline code inside one block of text. */
function inline(src: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  let rest = src;
  let n = 0;
  while (rest.length > 0) {
    const code = rest.indexOf("`");
    const bold = rest.indexOf("**");
    if (code < 0 && bold < 0) {
      out.push(rest);
      break;
    }
    const first = code < 0 ? bold : bold < 0 ? code : Math.min(code, bold);
    if (first > 0) out.push(rest.slice(0, first));
    if (first === code) {
      const end = rest.indexOf("`", first + 1);
      if (end < 0) {
        out.push(rest.slice(first));
        break;
      }
      out.push(
        <code key={`${key}-c${n++}`} className="rounded bg-[var(--color-sunk)] px-1 text-[0.9em]">
          {rest.slice(first + 1, end)}
        </code>,
      );
      rest = rest.slice(end + 1);
    } else {
      const end = rest.indexOf("**", first + 2);
      if (end < 0) {
        out.push(rest.slice(first));
        break;
      }
      out.push(<strong key={`${key}-b${n++}`}>{rest.slice(first + 2, end)}</strong>);
      rest = rest.slice(end + 2);
    }
  }
  return out;
}

const fenceRe = /^\s*```/;
const headingRe = /^(#{1,6})\s+(.*)$/;
const bulletRe = /^\s*[-*]\s+(.*)$/;
const orderedRe = /^\s*\d+[.)]\s+(.*)$/;

function startsBlock(line: string): boolean {
  return (
    fenceRe.test(line) ||
    headingRe.test(line) ||
    bulletRe.test(line) ||
    orderedRe.test(line)
  );
}

/** renderMarkdown turns one answer into block-level nodes. */
export function renderMarkdown(src: string): ReactNode[] {
  const lines = src.split("\n");
  const out: ReactNode[] = [];
  let i = 0;
  let k = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (fenceRe.test(line)) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !fenceRe.test(lines[i])) body.push(lines[i++]);
      // Past the end when the stream stopped inside the block. The content is
      // still code and is shown as code.
      i++;
      out.push(
        <pre
          key={k++}
          className="mt-3 overflow-x-auto rounded border border-[var(--color-hairline)] bg-[var(--color-sunk)] p-3 text-sm"
        >
          <code>{body.join("\n")}</code>
        </pre>,
      );
      continue;
    }

    const h = headingRe.exec(line);
    if (h) {
      // The page already owns h1, so # starts at h2 and the outline stays sane.
      const level = Math.min(h[1].length + 1, 4);
      const Tag = `h${level}` as keyof JSX.IntrinsicElements;
      out.push(
        <Tag key={k++} className="mt-4 font-semibold">
          {inline(h[2], `h${k}`)}
        </Tag>,
      );
      i++;
      continue;
    }

    if (line.trim() === "") {
      i++;
      continue;
    }

    const ordered = orderedRe.test(line);
    if (ordered || bulletRe.test(line)) {
      const items: ReactNode[] = [];
      while (i < lines.length) {
        const m = ordered ? orderedRe.exec(lines[i]) : bulletRe.exec(lines[i]);
        if (!m) break;
        items.push(
          <li key={items.length} className="ml-5 list-outside">
            {inline(m[1], `li${k}-${items.length}`)}
          </li>,
        );
        i++;
      }
      const List = ordered ? "ol" : "ul";
      out.push(
        <List key={k++} className={"mt-3 " + (ordered ? "list-decimal" : "list-disc")}>
          {items}
        </List>,
      );
      continue;
    }

    const para: string[] = [];
    while (i < lines.length && lines[i].trim() !== "" && !startsBlock(lines[i])) {
      para.push(lines[i++]);
    }
    out.push(
      <p key={k++} className="mt-3 whitespace-pre-wrap">
        {inline(para.join("\n"), `p${k}`)}
      </p>,
    );
  }

  return out;
}

/** Markdown renders one answer. */
export default function Markdown({ text }: { text: string }) {
  return <>{renderMarkdown(text)}</>;
}
