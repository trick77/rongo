import { useEffect, useRef, useState } from "react";
import { ThumbDownIcon, ThumbUpIcon } from "./icons";
import { groupByQuestion, type Turn } from "./turns";

/** The reader's verdict on a thread as the server keeps it. */
type Feedback = { verdict: 1 | -1; reason: string; upToMessageId: number };

/** What a thumbs down may say was off, wire value first. Chrome, so English
 * whatever the thread's language. */
const reasons: [string, string][] = [
  ["wrong", "Wrong"],
  ["incomplete", "Incomplete"],
  ["missed_code", "Missed the code"],
  ["too_long", "Too long"],
  ["wrong_repo", "Wrong repository"],
];

function asFeedback(v: unknown): Feedback | null {
  if (!v || typeof v !== "object" || Array.isArray(v)) return null;
  const f = v as Partial<Feedback>;
  if ((f.verdict !== 1 && f.verdict !== -1) || typeof f.upToMessageId !== "number") return null;
  return { verdict: f.verdict, reason: typeof f.reason === "string" ? f.reason : "", upToMessageId: f.upToMessageId };
}

/** A turn the server counts as a finished answer, the one thing a verdict can
 * cover: written, not failed, not a card asking back. */
export const finished = (t: Turn) =>
  t.done && t.text !== "" && !t.error && !t.clarification && t.messageId !== null;

/** The first turn holding an answer written after the verdict, or null when
 * the verdict covers everything on screen. By message id, not by position:
 * a re-explain of an older turn is newer than the turns below it, so the
 * newest answer the verdict covered can sit in turn 1 while turn 2 was on
 * screen too. Turns as the view counts them — a re-explain or a retry belongs
 * to the question it answers. */
function firstUncovered(turns: Turn[], upTo: number): number | null {
  const at = groupByQuestion(turns).findIndex((g) =>
    g.some((i) => finished(turns[i]) && (turns[i].messageId ?? 0) > upTo),
  );
  return at < 0 ? null : at + 1;
}

// The same face as the buttons beside them (Explain as…, Copy as Markdown,
// the follow-up chips): ink-dim at 13.5px on the panel, active on hover.
const thumb =
  "inline-flex h-7 w-6.5 items-center justify-center rounded-full text-ink-dim hover:bg-active hover:text-ink aria-pressed:bg-elevated aria-pressed:text-ink";
const chip =
  "rounded-full border border-border bg-panel px-3.5 py-1.5 text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active";

/**
 * The reader's verdict on the whole thread, drawn among the buttons under the
 * newest answer — where the reader stops reading. ThreadView puts it on that
 * one answer only, never while a turn runs, never on a shared page. Its pieces
 * are flex items of that button row: the reasons after a thumbs down take a
 * row of their own beneath it.
 */
export default function ThreadFeedback({ threadId, turns }: { threadId: string; turns: Turn[] }) {
  const [fb, setFb] = useState<Feedback | null>(null);
  // The picker belongs to the newest answer on screen when it was opened, and
  // shows only while that is still the newest. A reason picked after the next
  // answer landed would re-pin the verdict to an answer the reader never
  // judged — whatever order the save, the running turn and the answer arrive
  // in, a newer answer closes it.
  const [pickingAt, setPickingAt] = useState<number | null>(null);
  const newest = turns.reduce((m, t) => (finished(t) ? Math.max(m, t.messageId ?? 0) : m), 0);
  const picking = pickingAt !== null && pickingAt === newest;
  // The thread the state on screen belongs to; a reply for another one is late.
  const shown = useRef(threadId);
  // Set by the reader's first click on this thread. The load's reply can be
  // older than that click's write, and must not undo what the reader just did.
  const touched = useRef(false);

  useEffect(() => {
    shown.current = threadId;
    touched.current = false;
    setFb(null);
    setPickingAt(null);
    (async () => {
      try {
        const res = await fetch(`/api/threads/${threadId}/feedback`);
        if (!res.ok || shown.current !== threadId) return;
        const got = asFeedback(await res.json());
        if (shown.current === threadId && !touched.current) setFb(got);
      } catch {
        // No verdict on screen is what a failed read leaves: the thumbs still
        // work, and the next click writes the truth.
      }
    })();
  }, [threadId]);

  // A click shows at once; the server is told afterwards. Writes go one at a
  // time — two in flight can come back in the other order from the one the
  // server stored them in — and while one is out, further clicks only move
  // `want`, so the next write sends the reader's latest choice and the ones
  // in between are never sent. undefined: nothing waiting; null: clear.
  const want = useRef<{ verdict: 1 | -1; reason: string } | null | undefined>(undefined);
  const inflight = useRef(false);

  const reload = async () => {
    try {
      const res = await fetch(`/api/threads/${threadId}/feedback`);
      if (res.ok && shown.current === threadId) setFb(asFeedback(await res.json()));
    } catch {
      // Nothing better to show than what is on screen.
    }
  };

  const sync = async () => {
    if (inflight.current) return;
    inflight.current = true;
    try {
      while (want.current !== undefined) {
        const target = want.current;
        want.current = undefined;
        const res = await fetch(
          `/api/threads/${threadId}/feedback`,
          target === null
            ? { method: "DELETE" }
            : { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(target) },
        );
        if (!res.ok) throw new Error(`feedback ${res.status}`);
        // The server's own figures, unless the reader has moved on since.
        if (want.current === undefined && target !== null) setFb(asFeedback(await res.json()));
      }
    } catch {
      // A write the server refused leaves the screen claiming what it does
      // not hold: drop what was waiting and read the truth back.
      want.current = undefined;
      await reload();
    } finally {
      inflight.current = false;
    }
  };

  const vote = (verdict: 1 | -1) => {
    touched.current = true;
    if (fb?.verdict === verdict) {
      setFb(null);
      setPickingAt(null);
      want.current = null;
    } else {
      // Pinned to the newest answer, as the server will pin it. A thumbs down
      // offers the reasons; changing a reason is a thumbs up and down again.
      setFb({ verdict, reason: "", upToMessageId: newest });
      want.current = { verdict, reason: "" };
      setPickingAt(verdict === -1 ? newest : null);
    }
    void sync();
  };

  const pick = (reason: string) => {
    setFb({ verdict: -1, reason, upToMessageId: newest });
    want.current = { verdict: -1, reason };
    setPickingAt(null);
    void sync();
  };

  const uncovered = fb ? firstUncovered(turns, fb.upToMessageId) : null;

  // "Helpful?" as the question; "Helpful" once answered yes; the reason of a
  // thumbs down when it carries one.
  const label =
    fb?.verdict === 1
      ? "Helpful"
      : (fb?.verdict === -1 && reasons.find(([value]) => value === fb.reason)?.[1]) || "Helpful?";

  return (
    <>
      <span className="inline-flex items-center rounded-full border border-border bg-panel py-0.5 pr-0.5 pl-3.5">
        <span className="pr-1 text-[13.5px] text-ink-dim">{label}</span>
        <button
          type="button"
          aria-label="Helpful"
          title="Helpful"
          aria-pressed={fb?.verdict === 1}
          className={thumb}
          onClick={() => void vote(1)}
        >
          <ThumbUpIcon />
        </button>
        <button
          type="button"
          aria-label="Not helpful"
          title="Not helpful"
          aria-pressed={fb?.verdict === -1}
          className={thumb}
          onClick={() => void vote(-1)}
        >
          <ThumbDownIcon />
        </button>
      </span>
      {uncovered !== null && <span className="text-[13.5px] text-muted">rated before turn {uncovered}</span>}
      {/* No way to dismiss it: the thumbs down is already stored without a
          reason, and a reason is the reader's to add or not. */}
      {picking && (
        // order-last + basis-full: a row of its own under the buttons, below
        // the usage pill that ends the row above.
        <div className="order-last mt-1 flex basis-full flex-wrap items-center gap-2 text-[13.5px] text-ink-dim">
          <span className="mr-1">What was off?</span>
          {reasons.map(([value, text]) => (
            <button key={value} type="button" className={chip} onClick={() => void pick(value)}>
              {text}
            </button>
          ))}
        </div>
      )}
    </>
  );
}
