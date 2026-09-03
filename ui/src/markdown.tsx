import type { JSX, ReactNode } from "react";

/**
 * A small Markdown renderer covering exactly what the answer prompt produces:
 * headings, paragraphs, bold, inline code, fenced code and lists.
 *
 * It builds React nodes and never HTML. The text is model output, and
 * dangerouslySetInnerHTML would turn a prompt injection into a script tag.
 *
 * There is deliberately NO link syntax. Citation markers like [1] are what
 * makes an answer checkable; a renderer that consumed one — link parsing,
 * "tidy up stray brackets" — would break the evidence trail while looking
 * like it worked. A complete marker is STYLED as a superscript, never
 * consumed: the brackets stay in the text (visually hidden), so the rendered
 * text still reads "[1]" to a screen reader, a copy, or a test, and a
 * half-written "[1" mid-stream stays plain text until the bracket arrives.
 * A grouped marker "[1, 2]" is split into one complete "[n]" per number with
 * the separators kept between them, so it reads "[1], [2]"; a group with no
 * backed number at all is left verbatim, as any other unbacked marker.
 *
 * Which markers are real is the backend's call (citationsFor drops the ones
 * no source backs — from the citation list, not from the text). Once the
 * citations are known, a marker among them is a citation and an invented [7]
 * drops back to plain text, because a chip that looks checkable but leads
 * nowhere is the failure this renderer exists to prevent. While the answer
 * still streams and the list has not arrived, every marker already wears the
 * citation look: the list arrives last, and markers that change colour all at
 * once when the answer finishes read as a glitch, not as a verdict. Only the
 * hover hand-off to the Sources pane waits for the list, because until then
 * there is no row to point at.
 *
 * Unterminated markup renders as text rather than swallowing the rest. The
 * answer is re-rendered on every streamed token, so half-written markup is the
 * normal case here, not an edge case.
 */

/** One marker, or a grouped one: the prompt asks for [1][2], but a claim
 * resting on several sources still comes out as [1, 2] often enough. Read as
 * a single marker it matched nothing and stayed plain text next to chips. */
const markerRe = /\[(\d{1,3}(?:\s*,\s*\d{1,3})*)\]/g;

/** text renders one run of plain text, with complete citation markers as
 * superscripts, one per number of a grouped marker. onMarker lets the view
 * react to a marker being pointed at. */
function text(src: string, key: string, hooks: MarkerHooks): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let n = 0;
  const known = hooks.backed !== undefined;
  for (const m of src.matchAll(markerRe)) {
    const i = m.index ?? 0;
    if (i > last) out.push(src.slice(last, i));
    // Every number becomes a complete marker of its own, brackets included,
    // with the separators kept between them: a group [1, 2] reads "[1], [2]"
    // to a screen reader, a copy, or a test - each piece checkable alone.
    // On screen the chips sit side by side, as [1][2] would; a separator
    // stays visible only where a chip would otherwise touch plain text.
    const parts = m[1].split(/(\s*,\s*)/);
    const plain = (part: string) => known && !hooks.backed!.has(Number(part));
    if (parts.every((part, p) => p % 2 === 1 || plain(part))) {
      // Nothing behind any of it: plain text, as it came. Answers stored
      // before groups were read have no rows for their numbers.
      out.push(m[0]);
      last = i + m[0].length;
      continue;
    }
    parts.forEach((part, p) => {
      if (p % 2 === 1) {
        const visible = plain(parts[p - 1]) || plain(parts[p + 1]);
        out.push(visible ? part : <span key={`${key}-s${i}-${p}`} className="sr-only">{part}</span>);
        return;
      }
      const marker = Number(part);
      if (plain(part)) {
        // No source behind it: plain text.
        out.push(`[${part}]`);
        return;
      }
      out.push(
        <sup
          key={`${key}-m${n++}`}
          className="mx-px rounded bg-accent-dim px-1 font-mono text-[10px] font-semibold text-accent-strong"
          onMouseEnter={() => known && hooks.onHover?.(marker)}
          onMouseLeave={() => known && hooks.onHover?.(null)}
        >
          <span className="sr-only">[</span>
          {part}
          <span className="sr-only">]</span>
        </sup>,
      );
    });
    last = i + m[0].length;
  }
  if (last < src.length) out.push(src.slice(last));
  return out;
}

/** inline renders bold and inline code inside one block of text. */
function inline(src: string, key: string, hooks: MarkerHooks): ReactNode[] {
  const out: ReactNode[] = [];
  let rest = src;
  let n = 0;
  while (rest.length > 0) {
    const code = rest.indexOf("`");
    const bold = rest.indexOf("**");
    if (code < 0 && bold < 0) {
      out.push(...text(rest, `${key}-t${n++}`, hooks));
      break;
    }
    const first = code < 0 ? bold : bold < 0 ? code : Math.min(code, bold);
    if (first > 0) out.push(...text(rest.slice(0, first), `${key}-t${n++}`, hooks));
    if (first === code) {
      const end = rest.indexOf("`", first + 1);
      if (end < 0) {
        out.push(rest.slice(first));
        break;
      }
      out.push(
        <code
          key={`${key}-c${n++}`}
          className="rounded-[5px] border border-border bg-active px-1.5 text-[0.88em]"
        >
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
      out.push(
        <strong key={`${key}-b${n++}`} className="font-semibold text-ink">
          {text(rest.slice(first + 2, end), `${key}-bt${n}`, hooks)}
        </strong>,
      );
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

type MarkerHooks = {
  onHover?: (marker: number | null) => void;
  /** The markers a source backs, once the citations have arrived; undefined
   * while they are still unknown. */
  backed?: Set<number>;
};

/** renderMarkdown turns one answer into block-level nodes. */
export function renderMarkdown(src: string, hooks: MarkerHooks = {}): ReactNode[] {
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
          className="mt-3 overflow-x-auto rounded-ui-sm border border-border bg-panel p-3 font-mono text-[13px] leading-relaxed"
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
        <Tag key={k++} className="mt-5 font-serif text-xl font-medium text-ink">
          {inline(h[2], `h${k}`, hooks)}
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
            {inline(m[1], `li${k}-${items.length}`, hooks)}
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
        {inline(para.join("\n"), `p${k}`, hooks)}
      </p>,
    );
  }

  return out;
}

/** Markdown renders one answer. */
export default function Markdown({
  text: src,
  onMarkerHover,
  backed,
}: {
  text: string;
  onMarkerHover?: (marker: number | null) => void;
  /** Markers with a source behind them; leave undefined while unknown. */
  backed?: Set<number>;
}) {
  return <>{renderMarkdown(src, { onHover: onMarkerHover, backed })}</>;
}
