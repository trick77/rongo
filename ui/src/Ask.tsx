import { useEffect, useRef, useState } from "react";
import Markdown from "./markdown";
import Clarify, { type ClarifyCandidate } from "./Clarify";
import Trace, { type TraceState } from "./Trace";

type Citation = {
  marker: number;
  repo: string;
  branch: string;
  path: string;
  start_line: number;
  end_line: number;
};

/** The clarification a turn ended with, as this view needs it: the id of the
 * message that carries the card (used to resume it) and its candidates. */
type TurnClarification = {
  messageId: number;
  candidates: ClarifyCandidate[];
};

type Turn = {
  question: string;
  audience: "ba" | "dev";
  text: string;
  citations: Citation[];
  status: string;
  // Every "status" event in order, for the trace panel — not just the latest
  // one, which is all the old single status line kept.
  steps: string[];
  error: string;
  done: boolean;
  // The id of the message this turn was recorded as, once known. Reexplain
  // needs it; a turn still in flight has none yet.
  messageId: number | null;
  clarification: TurnClarification | null;
  // The candidate picked on this turn's card, or null before a choice. Once
  // set it stays — picking a different candidate later starts a NEW turn,
  // never rewriting this one.
  chosenIdx: number | null;
  // True for a turn created in this session. A turn restored from the
  // record carries no live trace — it is finished by definition.
  live: boolean;
};

/** Message is one stored turn, as GET /api/threads/{id} serves it. */
type Message = {
  id: number;
  ordinal: number;
  audience: string;
  question: string;
  answer: string;
  error: string;
  citations: Citation[] | null;
  clarification: { candidates: ClarifyCandidate[] } | null;
  from_candidate_idx: number;
};

/**
 * A stored turn renders exactly as it was answered — including the failure,
 * which stays in the record. It is finished by definition, so it carries no
 * live trace.
 */
function storedTurn(m: Message): Turn {
  return {
    question: m.question,
    audience: m.audience === "dev" ? "dev" : "ba",
    text: m.answer ?? "",
    citations: m.citations ?? [],
    status: "",
    steps: [],
    error: m.error ?? "",
    done: true,
    messageId: m.id,
    clarification: m.clarification ? { messageId: m.id, candidates: m.clarification.candidates } : null,
    chosenIdx: null,
    live: false,
  };
}

/**
 * A fresh, in-flight turn — asked, resumed from a candidate, or
 * re-explained. All three start the same way: no answer yet, no steps yet.
 */
function freshTurn(question: string, audience: "ba" | "dev"): Turn {
  return {
    question,
    audience,
    text: "",
    citations: [],
    status: "",
    steps: [],
    error: "",
    done: false,
    messageId: null,
    clarification: null,
    chosenIdx: null,
    live: true,
  };
}

/**
 * Stored turns carry no back-link from a clarification to the choice that
 * resolved it — only the resuming message's own from_candidate_idx. A
 * clarification is resolved by the very next message in the thread, since
 * resuming always continues the same thread right after asking.
 */
function linkChosenCandidates(list: Message[], turns: Turn[]): Turn[] {
  return turns.map((t, i) => {
    const next = list[i + 1];
    if (list[i].clarification && next && next.from_candidate_idx !== -1) {
      return { ...t, chosenIdx: next.from_candidate_idx };
    }
    return t;
  });
}

/** The trace's three states — the turn does not merely finish or not, it can
 * also end by asking, and that closes on the waiting node, not the check. */
function traceState(turn: Turn): TraceState {
  if (turn.clarification) return "waiting";
  return turn.done ? "done" : "running";
}

/**
 * Parses an SSE body incrementally. The whole point of streaming is that the
 * reader sees the first sentence before the last one exists, so this consumes
 * whatever has arrived and keeps the remainder for the next chunk.
 */
function drain(buffer: string, onEvent: (name: string, data: string) => void): string {
  const blocks = buffer.split("\n\n");
  const rest = blocks.pop() ?? "";
  for (const block of blocks) {
    let name = "";
    let data = "";
    for (const line of block.split("\n")) {
      if (line.startsWith("event: ")) name = line.slice(7);
      else if (line.startsWith("data: ")) data = line.slice(6);
    }
    if (name) onEvent(name, data);
  }
  return rest;
}

function forgeLine(c: Citation): string {
  return `${c.repo} · ${c.path}:${c.start_line}-${c.end_line} (${c.branch})`;
}

/**
 * Chevron, rotating 90 degrees on open. No triangle, no plus/minus, and the
 * same glyph in both states — only the rotation changes.
 */
export function Chevron({ open = false }: { open?: boolean }) {
  return (
    <svg
      className={"chev inline-block h-3 w-3 transition-transform " + (open ? "rotate-90" : "")}
      viewBox="0 0 12 12"
      aria-hidden="true"
    >
      <path
        d="M4.5 2.5 L8 6 L4.5 9.5"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export default function Ask({
  threadId: openThread = null,
  onThread = () => {},
  onActivity = () => {},
  onBusy = () => {},
}: {
  /** The thread to show, or null for a fresh one. */
  threadId?: number | null;
  /** Reports the thread this view is on; null means the id led nowhere. */
  onThread?: (id: number | null) => void;
  /** Something changed that the thread list should see. */
  onActivity?: () => void;
  /** Reports whether a turn is in flight, so the thread list can lock. */
  onBusy?: (busy: boolean) => void;
}) {
  const [question, setQuestion] = useState("");
  const [audience, setAudience] = useState<"ba" | "dev">("ba");
  const [turns, setTurns] = useState<Turn[]>([]);
  const [busy, setBusy] = useState(false);
  const threadId = useRef<number | null>(openThread);
  // shown is the thread whose turns are already on screen. Without it the
  // stream's own thread event — which travels up to the parent and back down as
  // a prop — would re-trigger the loader and replace the half-written answer
  // with the stored record, which does not have it yet.
  //
  // It starts undefined rather than at openThread: a thread restored from the
  // last session has to be LOADED on the first render, not assumed present.
  const shown = useRef<number | null | undefined>(undefined);
  // Which load is the current one. StrictMode mounts every effect twice, so a
  // cleanup that cancelled the in-flight request would cancel the ONLY request
  // — the second run sees the id as already shown and starts none. Cancelling
  // by sequence instead of by cleanup also settles the real race: switching
  // threads while a slower load is in the air.
  const loadSeq = useRef(0);

  function patchLast(patch: (t: Turn) => Turn) {
    setTurns((prev) => prev.map((t, i) => (i === prev.length - 1 ? patch(t) : t)));
  }

  // Announced upwards as well as kept locally: switching threads mid-stream
  // would swap the turn list under patchLast, and the tokens still arriving
  // would be written into the wrong conversation.
  function markBusy(b: boolean) {
    setBusy(b);
    onBusy(b);
  }

  useEffect(() => {
    if (openThread === shown.current) return;
    // Bumped FIRST, on every run — including the one that opens a fresh thread.
    // A load already in the air belongs to the thread that was just left, and
    // without this it would still be applied when it lands, putting the
    // previous conversation on screen under a different thread id.
    const seq = ++loadSeq.current;
    shown.current = openThread;
    threadId.current = openThread;
    if (openThread === null) {
      setTurns([]);
      return;
    }
    (async () => {
      try {
        const res = await fetch(`/api/threads/${openThread}`);
        if (!res.ok) {
          // 503, 500 and 401 mean "not right now", not "not yours". Treating
          // them as a dead thread would drop the bookmark on a passing blip and
          // the conversation would be gone on the next reload.
          if (seq === loadSeq.current) setTurns([]);
          return;
        }
        const list = await res.json();
        if (seq !== loadSeq.current) return;
        // A thread that is not yours, or no longer exists, comes back as an
        // empty list with status 200 — the owner check sits inside the query.
        // Rendering that as an empty thread would keep a dead id around
        // forever, so it is handed back as "no thread".
        if (!Array.isArray(list) || list.length === 0) {
          setTurns([]);
          shown.current = null;
          threadId.current = null;
          onThread(null);
          return;
        }
        setTurns(linkChosenCandidates(list, list.map(storedTurn)));
      } catch {
        // The turns are cleared rather than left standing: the ids have already
        // moved to the new thread, and a visible conversation that belongs to
        // the old one would send the next question somewhere the reader is not
        // looking.
        if (seq === loadSeq.current) setTurns([]);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openThread]);

  /**
   * Streams one turn's SSE response into the LAST entry of `turns`. Shared by
   * asking, resuming a clarification and re-explaining — all three post,
   * stream the same event vocabulary, and land in a turn appended just
   * before the call.
   */
  async function stream(url: string, body: object) {
    markBusy(true);
    try {
      const res = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok || !res.body) {
        patchLast((t) => ({ ...t, error: `Der Server antwortete mit ${res.status}.`, done: true }));
        return;
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        buffer = drain(buffer, (name, data) => {
          const payload = data ? JSON.parse(data) : {};
          if (name === "thread") {
            threadId.current = payload.thread_id;
            shown.current = payload.thread_id;
            onThread(payload.thread_id);
            // The placeholder title is written by Create, so the entry can
            // appear in the list the moment the question is sent.
            onActivity();
          } else if (name === "status") {
            patchLast((t) => ({ ...t, status: payload.step, steps: [...t.steps, payload.step] }));
          } else if (name === "token") patchLast((t) => ({ ...t, text: t.text + payload.text }));
          else if (name === "citations") patchLast((t) => ({ ...t, citations: payload ?? [] }));
          else if (name === "clarification") {
            patchLast((t) => ({
              ...t,
              messageId: payload.message_id,
              clarification: { messageId: payload.message_id, candidates: payload.candidates ?? [] },
            }));
          } else if (name === "error") patchLast((t) => ({ ...t, error: payload.message, done: true }));
          else if (name === "done") {
            patchLast((t) => ({
              ...t,
              done: true,
              status: "",
              messageId: payload.message_id ?? t.messageId,
            }));
            // The model-written title replaces the placeholder in a background
            // goroutine that has no way to push it here. Without this the
            // sidebar shows the truncated question until the next reload.
            onActivity();
          }
        });
      }
    } catch {
      patchLast((t) => ({ ...t, error: "Die Verbindung ist abgebrochen.", done: true }));
    } finally {
      markBusy(false);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const q = question.trim();
    if (!q || busy) return;

    // A thread load still in the air would replace the whole turn list when it
    // lands, dropping the turn appended just below — and every token after that
    // would be patched into the last STORED answer instead, corrupting a
    // finished turn on screen while this question disappeared. Retiring the
    // load is what the reader expects anyway: they have moved on.
    loadSeq.current++;

    setTurns((prev) => [...prev, freshTurn(q, audience)]);
    setQuestion("");

    await stream("/api/ask", { question: q, audience, thread_id: threadId.current ?? 0 });
  }

  /**
   * A choice on a clarification card does two things: it marks the card that
   * asked, and it starts a NEW turn for the answer. Nothing about the card
   * is overwritten — picking a different candidate later repeats exactly
   * this, appending yet another turn.
   */
  async function chooseCandidate(turnIndex: number, idx: number) {
    if (busy) return;
    const turn = turns[turnIndex];
    if (!turn.clarification) return;

    loadSeq.current++;
    setTurns((prev) => [
      ...prev.map((t, i) => (i === turnIndex ? { ...t, chosenIdx: idx } : t)),
      freshTurn(turn.question, turn.audience),
    ]);

    await stream("/api/ask", {
      thread_id: threadId.current ?? 0,
      question: turn.question,
      audience: turn.audience,
      clarification_message_id: turn.clarification.messageId,
      choice: idx,
    });
  }

  /**
   * Re-explaining is a NEW turn for the other audience, from sources a prior
   * turn already gathered — never a rewrite, and never a fresh /api/ask
   * question.
   */
  async function reexplain(turnIndex: number) {
    if (busy) return;
    const turn = turns[turnIndex];
    if (!turn.messageId) return;
    const nextAudience: "ba" | "dev" = turn.audience === "dev" ? "ba" : "dev";

    loadSeq.current++;
    setTurns((prev) => [...prev, freshTurn(turn.question, nextAudience)]);

    await stream(`/api/messages/${turn.messageId}/reexplain`, { audience: nextAudience });
  }

  return (
    <div>
      {turns.map((turn, i) => (
        <article key={i} className="mb-8 border-b border-[var(--color-hairline)] pb-6">
          <p className="font-medium">{turn.question}</p>
          <p className="mt-1 text-xs text-[var(--color-ink-faint)]">
            {turn.audience === "ba" ? "Business Analyst" : "Developer"}
          </p>

          {/* A restored turn is finished by definition and carries no live
              trace — only a turn asked, resumed or re-explained in THIS
              session does. */}
          {turn.live && <Trace steps={turn.steps} state={traceState(turn)} />}

          {turn.clarification && (
            <Clarify
              candidates={turn.clarification.candidates}
              chosenIdx={turn.chosenIdx}
              onChoose={(idx) => chooseCandidate(i, idx)}
            />
          )}

          {turn.text && <Markdown text={turn.text} />}

          {turn.error && (
            <p role="alert" className="mt-3 text-[var(--color-ochre)]">
              {turn.error}
            </p>
          )}

          {turn.citations.length > 0 && (
            <details className="mt-4 text-sm">
              <summary className="cursor-pointer text-[var(--color-ink-soft)]">
                <Chevron /> Woher weiss rongo das?{" "}
                <span className="text-[var(--color-ink-faint)]">
                  {turn.citations.length} Belege
                </span>
              </summary>
              <ul className="mt-2 space-y-1">
                {turn.citations.map((c) => (
                  <li key={c.marker}>
                    <sup>{c.marker}</sup> <code>{forgeLine(c)}</code>
                  </li>
                ))}
              </ul>
            </details>
          )}

          {/* Re-explaining needs a stored answer to build from — never on a
              turn that failed or ended by asking. */}
          {turn.done && turn.messageId && !turn.error && !turn.clarification && (
            <button
              type="button"
              disabled={busy}
              onClick={() => reexplain(i)}
              className="mt-3 rounded border border-[var(--color-hairline)] px-3 py-1 text-sm text-[var(--color-ink-soft)] hover:text-[var(--color-ink)] disabled:opacity-50"
            >
              {turn.audience === "dev" ? "Als BA neu erklären" : "Als Dev neu erklären"}
            </button>
          )}
        </article>
      ))}

      <form onSubmit={submit} className="sticky bottom-0 bg-[var(--color-ground)] pt-2">
        <textarea
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          rows={3}
          aria-label="Frage"
          placeholder="Wie wird die Teaser-Mail verschickt?"
          className="w-full rounded border border-[var(--color-hairline)] bg-[var(--color-surface)] p-3"
        />
        <div className="mt-2 flex items-center gap-3">
          <fieldset className="flex gap-1" aria-label="Rolle">
            {(["ba", "dev"] as const).map((role) => (
              <button
                key={role}
                type="button"
                aria-pressed={audience === role}
                onClick={() => setAudience(role)}
                className={
                  "rounded border px-3 py-1 text-sm " +
                  (audience === role
                    ? "border-[var(--color-accent)] text-[var(--color-accent)]"
                    : "border-[var(--color-hairline)] text-[var(--color-ink-soft)]")
                }
              >
                {role.toUpperCase()}
              </button>
            ))}
          </fieldset>
          <button
            type="submit"
            disabled={busy}
            className="rounded bg-[var(--color-accent)] px-4 py-1 text-sm text-white disabled:opacity-50"
          >
            Fragen
          </button>
        </div>
      </form>
    </div>
  );
}
