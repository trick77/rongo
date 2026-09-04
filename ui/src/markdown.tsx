import type { JSX, ReactNode } from "react";
import { highlightBlock, languageOf } from "./highlight";

/**
 * A small Markdown renderer covering exactly what the answer prompt produces:
 * headings, paragraphs, bold, inline code, fenced code (coloured by its
 * language tag, see highlight.tsx) and lists.
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
 * hover hand-off to the Sources pane and the tap that opens the source wait
 * for the list, because until then there is no row to point at and no file
 * to open.
 *
 * Unterminated markup renders as text rather than swallowing the rest. The
 * answer is re-rendered on every streamed token, so half-written markup is the
 * normal case here, not an edge case.
 */

/** How long a segment of the streaming fade runs before it is closed. Text
 * arrives token by token, but fading per token would flicker, so segments end
 * at a clause character or once they are long enough — the same coarse
 * granularity ../loom uses (ui/src/chat/streamFade.ts). */
const maxSegChars = 28;

/** splitIntoSegments cuts one run of prose into the pieces that fade in
 * separately. Whitespace stays attached to the word before it, so joining the
 * segments returns the text unchanged. */
export function splitIntoSegments(value: string): string[] {
  const tokens = value.match(/\S+\s*|\s+/g);
  if (!tokens) return [value];
  const out: string[] = [];
  let current = "";
  for (const tok of tokens) {
    current += tok;
    if (/[.!?,;:—)\]]$/.test(tok.trimEnd()) || current.length >= maxSegChars) {
      out.push(current);
      current = "";
    }
  }
  if (current !== "") out.push(current);
  return out;
}

/** faded wraps one run of prose in the spans that carry the fade.
 *
 * Keyed by where the segment starts in the whole block, never by its position
 * among the runs. The answer re-renders on every token, and a span that
 * remounted would restart its animation, so settled text would flicker for as
 * long as the answer keeps growing. An offset survives the event that shifts
 * the runs around: the moment a marker's closing bracket arrives, the prose
 * before it stops being the tail of the block and becomes a run of its own,
 * and only an absolute offset keeps those segments the same elements.
 *
 * Code and citation chips are deliberately NOT wrapped: a chip re-fading on
 * every token flickers, and a span inside a highlighted block would fight
 * the grammar's own colouring. */
function faded(src: string, key: string, base: number): ReactNode[] {
  let at = base;
  return splitIntoSegments(src).map((seg) => {
    const span = (
      <span key={`${key}-w${at}`} className="stream-seg">
        {seg}
      </span>
    );
    at += seg.length;
    return span;
  });
}

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
    if (i > last) out.push(...faded(src.slice(last, i), key, last));
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
      // Once its source is known the chip is a button that opens it: on a
      // tablet there is no hover and no pane, and a chip that cannot be
      // tapped leaves the reader no way to a source but a collapsed list
      // under the answer. The brackets stay in the text either way.
      const open = known && hooks.onOpen;
      out.push(
        <sup
          key={`${key}-m${n++}`}
          className="mx-px rounded bg-accent-dim px-1 font-mono text-[10px] font-semibold text-accent-strong"
          onMouseEnter={() => known && hooks.onHover?.(marker)}
          onMouseLeave={() => known && hooks.onHover?.(null)}
        >
          <span className="sr-only">[</span>
          {open ? (
            <button
              type="button"
              onClick={() => hooks.onOpen?.(marker)}
              // sup resets line-height to 0, which would give the button no
              // height at all; the padding widens the hit area for a finger
              // without moving the text around it.
              className="-mx-1 -my-2 cursor-pointer px-1 py-2 leading-none text-inherit hover:underline"
            >
              {part}
            </button>
          ) : (
            part
          )}
          <span className="sr-only">]</span>
        </sup>,
      );
    });
    last = i + m[0].length;
  }
  if (last < src.length) out.push(...faded(src.slice(last), key, last));
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

// The info string after the opening fence names the language; a closing fence
// has none and the group stays empty.
const fenceRe = /^\s*```\s*([\w+#-]*)/;
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
  /** Opens the source behind a marker; the chip is a button only once
   * `backed` says there is one. */
  onOpen?: (marker: number) => void;
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

    const fence = fenceRe.exec(line);
    if (fence) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !fenceRe.test(lines[i])) body.push(lines[i++]);
      // Past the end when the stream stopped inside the block. The content is
      // still code and is shown as code.
      i++;
      // Coloured straight from the grammar, never through text(): a marker-
      // shaped a[1] inside code is code, as the backend's withoutCode agrees.
      out.push(
        <pre
          key={k++}
          className="mt-3 overflow-x-auto rounded-ui-sm border border-border bg-panel p-3 font-mono text-[13px] leading-relaxed"
        >
          <code>{highlightBlock(body.join("\n"), languageOf(fence[1]))}</code>
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
  onMarkerOpen,
  backed,
}: {
  text: string;
  onMarkerHover?: (marker: number | null) => void;
  /** Called with the marker when a backed chip is clicked or tapped. */
  onMarkerOpen?: (marker: number) => void;
  /** Markers with a source behind them; leave undefined while unknown. */
  backed?: Set<number>;
}) {
  return <>{renderMarkdown(src, { onHover: onMarkerHover, onOpen: onMarkerOpen, backed })}</>;
}
