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

const finished = (t: Turn) => t.done && t.text !== "" && !t.error && t.messageId !== null;

/** Which turn the verdict was given at, and how many finished turns came
 * after it. Turns as the view counts them: a re-explain or a retry is part of
 * the question it answers, not a newer one. */
function coverage(turns: Turn[], upTo: number): { at: number; newer: number } | null {
  const groups = groupByQuestion(turns);
  const at = groups.findIndex((g) => g.some((i) => turns[i].messageId === upTo));
  if (at < 0) return null;
  const newer = groups.slice(at + 1).filter((g) => g.some((i) => finished(turns[i]))).length;
  return { at: at + 1, newer };
}

const thumb =
  "inline-flex h-6.5 w-6.5 items-center justify-center rounded-full text-faint hover:bg-active hover:text-ink aria-pressed:bg-elevated aria-pressed:text-ink";
const chip =
  "rounded-full border border-border bg-panel px-2.5 py-0.5 text-xs text-muted hover:border-elevated-border hover:text-ink";
const link = "text-muted underline decoration-border underline-offset-3 hover:text-ink";

/**
 * The line under the composer: the caveat, and once the thread holds a
 * finished answer and nothing is running, the reader's verdict on the whole
 * thread beside it. A thumbs down offers what was off in the same line, so
 * the composer's foot never grows a row. Owner only: the share page draws no
 * composer and the public share API serves no verdict.
 */
export default function ThreadFeedback({
  threadId,
  turns,
  running,
  caveat,
}: {
  threadId: string | null;
  turns: Turn[];
  running: boolean;
  caveat: string;
}) {
  const [fb, setFb] = useState<Feedback | null>(null);
  const [picking, setPicking] = useState(false);
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
    setPicking(false);
    setThanks(false);
    if (threadId === null) return;
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

  const vote = async (verdict: 1 | -1) => {
    touched.current = true;
    if (fb?.verdict === verdict) return clear();
    if (!(await put(verdict, ""))) return;
    if (verdict === -1) setPicking(true);
    else {
      setPicking(false);
      thank();
    }
  };

  const pick = async (reason: string) => {
    if (await put(-1, reason)) {
      setPicking(false);
      thank();
    }
  };

  const eligible = threadId !== null && !running && turns.some(finished);
  // min-h holds the line at the thumbs' height in every state, so the composer
  // above it does not jump when the thumbs appear or the reasons replace them.
  const wrap =
    "mt-3 flex min-h-6.5 flex-wrap items-center justify-center gap-x-2 gap-y-1 text-center text-xs text-faint [@media(max-height:500px)]:hidden";

  if (!eligible) return <p className={wrap}>{caveat}</p>;

  if (picking) {
    return (
      <div className={wrap}>
        <span>What was off?</span>
        {reasons.map(([value, label]) => (
          <button key={value} type="button" className={chip} onClick={() => void pick(value)}>
            {label}
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
    );
  }

  // Short on purpose: the line shares its width with the caveat and must not
  // wrap. The pressed thumb already says which way the verdict went, so the
  // words only add what it cannot — the reason, and that later turns came.
  let said = "Was this thread helpful?";
  if (fb) {
    const parts = [fb.reason ? (reasonLabel.get(fb.reason) ?? fb.reason) : fb.verdict === 1 ? "Helpful" : "Not helpful"];
    const cov = coverage(turns, fb.upToMessageId);
    if (cov && cov.newer > 0) parts.push(`rated before turn ${cov.at + 1}`);
    said = parts.join(" · ");
  }

  return (
    <div className={wrap}>
      <span>{caveat}</span>
      <span aria-hidden="true" className="text-border">
        ·
      </span>
      {!fb && <span>{said}</span>}
      <span className="inline-flex items-center">
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
      {fb && <span className="text-muted">{said}</span>}
      {fb?.verdict === -1 && (
        <>
          <span className="text-muted">·</span>
          <button type="button" className={link} onClick={() => setPicking(true)}>
            change
          </button>
        </>
      )}
      {thanks && <span className="text-muted">Thanks</span>}
    </div>
  );
}
