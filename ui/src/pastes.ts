/**
 * A long paste is a chip, not prose.
 *
 * A stack trace dropped into the composer used to land in the textarea and
 * then at the head of the turn, set as the reader's own words. Past a
 * threshold it is staged beside the textarea instead, sent folded into the
 * question — the model, the search and the title read it there, unchanged —
 * and described once more on the side, so the page knows which trailing part
 * of the question to fold back into a chip. ../loom does the same
 * (ui/src/chat/pastedText.ts); the thresholds are its.
 */

/** A paste folds into a chip past either of these. */
export const PASTE_CHAR_THRESHOLD = 2000;
export const PASTE_LINE_THRESHOLD = 25;

/**
 * The most a question may weigh, typed text and pastes together, in UTF-8
 * BYTES — the unit the server counts in (maxQuestionBytes in
 * backend/internal/httpapi/ask.go). Never String.length: "ä" is one there and
 * two here, and a German log that passed the client would bounce off the
 * server.
 */
export const MAX_QUESTION_BYTES = 32768;

/** One pasted block as it travels: the text and its line count, for the chip. */
export type PastedText = { text: string; lines: number };

/** Lines, not newlines: "one" is one line and "a\nb" is two. */
export function countLines(text: string): number {
  let n = 1;
  for (let i = text.indexOf("\n"); i !== -1; i = text.indexOf("\n", i + 1)) n++;
  return n;
}

/** Whether a paste is a chip rather than text. Measured trimmed: nearly every clipboard ends in a newline. */
export function shouldCollapse(text: string): boolean {
  const t = text.trim();
  return t.length > PASTE_CHAR_THRESHOLD || countLines(t) > PASTE_LINE_THRESHOLD;
}

const encoder = new TextEncoder();

export function byteLength(text: string): number {
  return encoder.encode(text).length;
}

/** A paste as it is staged: trimmed, so fold and strip agree with a server that trims the question. */
export function stagePaste(text: string): PastedText {
  const t = text.trim();
  return { text: t, lines: countLines(t) };
}

const SEP = "\n\n";

/** The question as sent: what was typed, then each paste, a blank line between. */
export function fold(typed: string, pastes: PastedText[]): string {
  return [typed.trim(), ...pastes.map((p) => p.text)].filter((s) => s !== "").join(SEP);
}

/**
 * The inverse of fold, for drawing a stored turn: peels the pastes off the
 * tail of the question, last first, and hands back what was typed. A block
 * that is not where fold put it — a row from before the fold was stored, or
 * one edited by hand — stays in the prose and is marked unmatched, so nothing
 * is ever drawn twice. Peeling stops at the first miss: everything before it
 * is, by construction, not at the tail either.
 */
export function strip(question: string, pastes: PastedText[]): { typed: string; matched: boolean[] } {
  let rest = question;
  const matched = pastes.map(() => false);
  for (let i = pastes.length - 1; i >= 0; i--) {
    const block = pastes[i].text.trim();
    if (block === "" || !rest.endsWith(block)) break;
    rest = rest.slice(0, rest.length - block.length);
    if (rest.endsWith(SEP)) rest = rest.slice(0, rest.length - SEP.length);
    matched[i] = true;
  }
  return { typed: rest.trim(), matched };
}
