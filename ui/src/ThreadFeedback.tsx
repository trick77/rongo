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
const reasonLabel = new Map(reasons);

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

const thumb =
  "inline-flex h-7 w-7 items-center justify-center rounded-full text-muted hover:bg-active hover:text-ink aria-pressed:bg-elevated aria-pressed:text-ink";
const chip =
  "rounded-full border border-border bg-panel px-3 py-1 text-[12.5px] text-muted hover:border-elevated-border hover:text-ink";
const link = "text-[12.5px] text-muted underline decoration-border underline-offset-3 hover:text-ink";

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
  const setPicking = (open: boolean) => setPickingAt(open ? newest : null);
  const [thanks, setThanks] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
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
    setThanks(false);
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

  useEffect(() => () => clearTimeout(timer.current), []);

  const thank = () => {
    setThanks(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setThanks(false), 2000);
  };

  const put = async (verdict: 1 | -1, reason: string): Promise<boolean> => {
    const id = threadId;
    try {
      const res = await fetch(`/api/threads/${id}/feedback`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ verdict, reason }),
      });
      if (!res.ok || shown.current !== id) return false;
      setFb(asFeedback(await res.json()));
      return true;
    } catch {
      return false;
    }
  };

  const clear = async () => {
    const id = threadId;
    try {
      const res = await fetch(`/api/threads/${id}/feedback`, { method: "DELETE" });
      if (res.ok && shown.current === id) {
        setFb(null);
        setPicking(false);
      }
    } catch {
      // Left as it was: the pressed thumb still says what the server holds.
    }
  };

  // One write at a time. Two in flight can come back in the other order from
  // the one the server stored them in, and the screen would then show the
  // verdict the server dropped. A ref, not state: a double click lands before
  // any re-render could disable the buttons.
  const saving = useRef(false);
  const once = async (write: () => Promise<void>) => {
    if (saving.current) return;
    saving.current = true;
    try {
      await write();
    } finally {
      saving.current = false;
    }
  };

  const vote = (verdict: 1 | -1) =>
    once(async () => {
      touched.current = true;
      if (fb?.verdict === verdict) return clear();
      // Taken at the click, not after the save: the answer the reader judged.
      const judged = newest;
      if (!(await put(verdict, ""))) return;
      if (verdict === -1) setPickingAt(judged);
      else {
        setPickingAt(null);
        thank();
      }
    });

  const pick = (reason: string) =>
    once(async () => {
      if (await put(-1, reason)) {
        setPicking(false);
        thank();
      }
    });

  // The pressed thumb already says which way the verdict went; the label adds
  // the reason when there is one.
  let label = "Helpful?";
  if (thanks) label = "Thanks";
  else if (fb) label = fb.reason ? (reasonLabel.get(fb.reason) ?? fb.reason) : fb.verdict === 1 ? "Helpful" : "Not helpful";
  const uncovered = fb ? firstUncovered(turns, fb.upToMessageId) : null;

  return (
    <>
      <span className="inline-flex items-center gap-0.5 rounded-full border border-border bg-panel py-0.5 pr-0.5 pl-3">
        <span className="pr-1.5 text-[12.5px] text-faint">{label}</span>
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
      {uncovered !== null && <span className="text-[12.5px] text-faint">rated before turn {uncovered}</span>}
      {/* A reason saved now is pinned to the newest answer, so "change" is
          offered only while the verdict already covers it. Past that, the
          thumbs are the way to rate again. */}
      {fb?.verdict === -1 && fb.upToMessageId === newest && !picking && (
        <button type="button" className={link} onClick={() => setPicking(true)}>
          {fb.reason ? "change reason" : "add a reason"}
        </button>
      )}
      {picking && (
        // order-last + basis-full: a row of its own under the buttons, below
        // the usage pill that ends the row above.
        <div className="order-last mt-1 flex basis-full flex-wrap items-center gap-1.5 text-[12.5px] text-faint">
          <span className="mr-1">What was off?</span>
          {reasons.map(([value, text]) => (
            <button key={value} type="button" className={chip} onClick={() => void pick(value)}>
              {text}
            </button>
          ))}
          <button
            type="button"
            className={link}
            onClick={() => {
              setPicking(false);
              thank();
            }}
          >
            skip
          </button>
        </div>
      )}
    </>
  );
}
