import { useEffect, useRef, useState } from "react";
import Markdown from "./markdown";
import Clarify, { type ClarifyCandidate } from "./Clarify";
import Trace, { type Step, type TraceState } from "./Trace";
import { Chevron } from "./icons";
import SourceView, { type SourceRef } from "./SourceView";

type Citation = SourceRef;

type Audience = "ba" | "dev";

/** The languages the answer can be written in; the backend's allowlist. */
export const languages: { code: string; name: string }[] = [
  { code: "en", name: "English" },
  { code: "de", name: "Deutsch" },
  { code: "fr", name: "Français" },
  { code: "it", name: "Italiano" },
];

/** The clarification a turn ended with, as this view needs it: the id of the
 * message that carries the card (used to resume it) and its candidates. */
type TurnClarification = {
  messageId: number;
  candidates: ClarifyCandidate[];
};

type Turn = {
  question: string;
  audience: Audience;
  language: string;
  text: string;
  citations: Citation[];
  // Every "status" event in order, with its arrival time, for the timeline.
  steps: Step[];
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
  // When the turn was sent and when it closed, for the timeline's totals.
  startedAt: number;
  endedAt: number | null;
  // Tokens the model spent on this turn, from the usage event; 0 if unknown.
  tokens: number;
  // The moment the question was asked, as the record has it, or now.
  askedAt: string;
};

/** Message is one stored turn, as GET /api/threads/{id} serves it. */
type Message = {
  id: number;
  ordinal: number;
  audience: string;
  language?: string;
  question: string;
  answer: string;
  error: string;
  citations: Citation[] | null;
  clarification: { id: number; candidates: ClarifyCandidate[] } | null;
  from_candidate_idx: number;
  // The clarification this message resolved, or 0 when it did not resume
  // one. The link the backend actually stored — matching on it, not on
  // position, is what tells two clarifications open in the same thread
  // apart.
  from_clarification_id: number;
  created_at?: string;
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
    language: m.language ?? "en",
    text: m.answer ?? "",
    citations: m.citations ?? [],
    steps: [],
    error: m.error ?? "",
    done: true,
    messageId: m.id,
    clarification: m.clarification ? { messageId: m.id, candidates: m.clarification.candidates } : null,
    chosenIdx: null,
    live: false,
    startedAt: 0,
    endedAt: 0,
    tokens: 0,
    askedAt: m.created_at ?? "",
  };
}

/**
 * A fresh, in-flight turn — asked, resumed from a candidate, or
 * re-explained. All three start the same way: no answer yet, no steps yet.
 */
function freshTurn(question: string, audience: Audience, language: string): Turn {
  const now = Date.now();
  return {
    question,
    audience,
    language,
    text: "",
    citations: [],
    steps: [],
    error: "",
    done: false,
    messageId: null,
    clarification: null,
    chosenIdx: null,
    live: true,
    startedAt: now,
    endedAt: null,
    tokens: 0,
    askedAt: new Date(now).toISOString(),
  };
}

/**
 * Marks each clarification's chosen candidate from the link the backend
 * actually stored (from_clarification_id → from_candidate_idx), never from
 * position: two clarifications can be open in the same thread at once, and
 * the older one can be the one resolved second, so "the next message"
 * points at the wrong card.
 */
function linkChosenCandidates(list: Message[], turns: Turn[]): Turn[] {
  const chosenByClarification = new Map<number, number>();
  for (const m of list) {
    if (m.from_clarification_id) chosenByClarification.set(m.from_clarification_id, m.from_candidate_idx);
  }
  return turns.map((t, i) => {
    const clarId = list[i].clarification?.id;
    if (clarId == null) return t;
    const idx = chosenByClarification.get(clarId);
    return idx === undefined ? t : { ...t, chosenIdx: idx };
  });
}

/** The trace's states — the turn does not merely finish or not: it can end
 * by asking (and that closes on the waiting node, not the check), lose the
 * colour once the reader has chosen, or break. */
function traceState(turn: Turn): TraceState {
  if (turn.error) return "failed";
  if (turn.clarification) return turn.chosenIdx == null ? "waiting" : "decided";
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

function roleName(a: Audience): string {
  return a === "dev" ? "Developer" : "Analyst";
}

function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" });
}

/** asMarkdown is what "Copy as Markdown" puts on the clipboard: the raw
 * answer with its markers, and the sources they point at. */
function asMarkdown(turn: Turn): string {
  const lines = [`# ${turn.question}`, "", turn.text.trim()];
  if (turn.citations.length > 0) {
    lines.push("", "Sources:", ...turn.citations.map((c) => `[${c.marker}] ${forgeLine(c)}`));
  }
  return lines.join("\n") + "\n";
}

const pill = "rounded-full px-2.5 py-0.5 text-xs whitespace-nowrap";

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
  const [audience, setAudience] = useState<Audience>("ba");
  const [language, setLanguage] = useState("en");
  const [turns, setTurns] = useState<Turn[]>([]);
  const [busy, setBusy] = useState(false);
  // The marker under the pointer, so the Sources pane can point back.
  const [hot, setHot] = useState<number | null>(null);
  // The source open in the viewer, or null while it is closed.
  const [viewing, setViewing] = useState<Citation | null>(null);
  const [copied, setCopied] = useState<number | null>(null);
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
  const bottom = useRef<HTMLDivElement>(null);

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

  // A new turn scrolls into view; tokens arriving into it do not fight the
  // reader who scrolled up.
  useEffect(() => {
    bottom.current?.scrollIntoView?.({ block: "end" });
  }, [turns.length, openThread]);

  // The composer grows with the question up to a few lines, so a multi-line
  // question can be read back before it is sent.
  const box = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 200) + "px";
  }, [question]);

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
        patchLast((t) => ({ ...t, error: `The server answered with ${res.status}.`, done: true, endedAt: Date.now() }));
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
            patchLast((t) => ({ ...t, steps: [...t.steps, { step: payload.step, at: Date.now() }] }));
          } else if (name === "token") patchLast((t) => ({ ...t, text: t.text + payload.text }));
          else if (name === "citations") patchLast((t) => ({ ...t, citations: payload ?? [] }));
          else if (name === "usage") patchLast((t) => ({ ...t, tokens: Number(payload.total_tokens ?? 0) }));
          else if (name === "clarification") {
            patchLast((t) => ({
              ...t,
              messageId: payload.message_id,
              clarification: { messageId: payload.message_id, candidates: payload.candidates ?? [] },
            }));
          } else if (name === "error") patchLast((t) => ({ ...t, error: payload.message, done: true, endedAt: Date.now() }));
          else if (name === "done") {
            patchLast((t) => ({
              ...t,
              done: true,
              endedAt: t.endedAt ?? Date.now(),
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
      patchLast((t) => ({ ...t, error: "The connection was lost.", done: true, endedAt: Date.now() }));
    } finally {
      markBusy(false);
    }
  }

  async function submit(e?: React.FormEvent) {
    e?.preventDefault();
    const q = question.trim();
    if (!q || busy) return;

    // A thread load still in the air would replace the whole turn list when it
    // lands, dropping the turn appended just below — and every token after that
    // would be patched into the last STORED answer instead, corrupting a
    // finished turn on screen while this question disappeared. Retiring the
    // load is what the reader expects anyway: they have moved on.
    loadSeq.current++;

    setTurns((prev) => [...prev, freshTurn(q, audience, language)]);
    setQuestion("");

    await stream("/api/ask", { question: q, audience, language, thread_id: threadId.current ?? 0 });
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
      freshTurn(turn.question, turn.audience, turn.language),
    ]);

    await stream("/api/ask", {
      thread_id: threadId.current ?? 0,
      question: turn.question,
      audience: turn.audience,
      language: turn.language,
      clarification_message_id: turn.clarification.messageId,
      choice: idx,
    });
  }

  /**
   * Re-explaining is a NEW turn for the other audience, from sources a prior
   * turn already gathered — never a rewrite, and never a fresh /api/ask
   * question. The language is inherited on the server from the turn it
   * re-answers.
   */
  async function reexplain(turnIndex: number) {
    if (busy) return;
    const turn = turns[turnIndex];
    if (!turn.messageId) return;
    const nextAudience: Audience = turn.audience === "dev" ? "ba" : "dev";

    loadSeq.current++;
    setTurns((prev) => [...prev, freshTurn(turn.question, nextAudience, turn.language)]);

    await stream(`/api/messages/${turn.messageId}/reexplain`, { audience: nextAudience });
  }

  async function copy(turnIndex: number) {
    try {
      await navigator.clipboard.writeText(asMarkdown(turns[turnIndex]));
      setCopied(turnIndex);
      setTimeout(() => setCopied(null), 1500);
    } catch {
      // No clipboard (insecure context, permissions): the button simply does
      // nothing visible; the answer is still on screen to select.
    }
  }

  // The Sources pane shows the latest turn that has any: the one the reader
  // is most likely looking at, and the only one whose markers can be pointed
  // back to.
  const sourceTurnIndex = (() => {
    for (let i = turns.length - 1; i >= 0; i--) if (turns[i].citations.length > 0) return i;
    return -1;
  })();
  const sourceTurn = sourceTurnIndex >= 0 ? turns[sourceTurnIndex] : null;

  // A highlight belongs to the turn the pane shows. When the pane moves to a
  // newer turn, the old Markdown's mouseleave never fires for it.
  useEffect(() => {
    setHot(null);
  }, [sourceTurnIndex]);

  return (
    // The Sources pane takes a fixed column only when there is room for it;
    // below that the thread has the width and the per-answer details block
    // still lists the sources.
    <div className="grid h-full min-h-0 grid-cols-1 xl:grid-cols-[1fr_300px] 2xl:grid-cols-[1fr_340px]">
      <div className="relative flex min-h-0 min-w-0 flex-col">
        {busy && <div className="busybar" aria-hidden="true" />}
        <div className="min-h-0 flex-1 overflow-auto">
          <div className="max-w-[900px] px-10 pt-8 pb-10">
            {turns.length === 0 && (
              <div className="mt-16 max-w-[52ch]">
                <h2 className="font-serif text-[28px] font-medium leading-tight tracking-tight text-ink">
                  Ask about the code.
                </h2>
                <p className="mt-3 text-muted">
                  rongo searches the indexed repositories, asks back when a question fits more than one
                  mechanism, and answers with sources for every claim. Pick a role: an Analyst gets the
                  mechanism in domain terms, a Developer gets types, functions and files.
                </p>
              </div>
            )}

            {turns.map((turn, i) => (
              <article
                key={i}
                className="mb-8 border-b border-border-soft pb-8 last:mb-0 last:border-b-0"
              >
                <div className="text-[11px] font-medium uppercase tracking-[.1em] text-accent-strong">
                  {roleName(turn.audience)}
                </div>
                <p className="mt-1.5 max-w-[30ch] font-serif text-[26px] font-medium leading-[1.3] tracking-tight text-ink text-balance">
                  {turn.question}
                </p>
                <div className="mt-2.5 flex items-center gap-1.5">
                  {turn.askedAt && <time className="font-mono text-[11.5px] text-faint">{clock(turn.askedAt)}</time>}
                  <span className={pill + " bg-active text-muted"}>turn {i + 1}</span>
                  {turn.language !== "en" && (
                    <span className={pill + " bg-active text-muted"}>
                      {languages.find((l) => l.code === turn.language)?.name ?? turn.language}
                    </span>
                  )}
                </div>

                {/* A restored turn is finished by definition and carries no live
                    trace — only a turn asked, resumed or re-explained in THIS
                    session does. */}
                {turn.live && (
                  <Trace steps={turn.steps} state={traceState(turn)} startedAt={turn.startedAt} endedAt={turn.endedAt} />
                )}

                {turn.clarification && (
                  <Clarify
                    candidates={turn.clarification.candidates}
                    chosenIdx={turn.chosenIdx}
                    onChoose={(idx) => chooseCandidate(i, idx)}
                  />
                )}

                {turn.text && (
                  <div className="mt-4 max-w-[68ch] text-base leading-[1.65] text-ink-dim">
                    <Markdown
                      text={turn.text}
                      onMarkerHover={i === sourceTurnIndex ? setHot : undefined}
                      // Known once the turn is done: the citations event is
                      // the last thing before done, so a finished turn with
                      // none has none.
                      backed={turn.done ? new Set(turn.citations.map((c) => c.marker)) : undefined}
                    />
                    {!turn.done && <span className="caret" aria-hidden="true" />}
                  </div>
                )}

                {turn.error && (
                  <p role="alert" className="mt-3 text-accent-strong">
                    {turn.error}
                  </p>
                )}

                {turn.citations.length > 0 && (
                  <details className="mt-4 text-sm">
                    <summary className="flex cursor-pointer list-none items-center gap-2 text-muted hover:text-ink [&::-webkit-details-marker]:hidden">
                      <Chevron /> How does rongo know this?{" "}
                      <span className="text-faint">{turn.citations.length} sources</span>
                    </summary>
                    <ul className="mt-2 space-y-1">
                      {turn.citations.map((c) => (
                        <li key={c.marker}>
                          <sup className="font-mono text-accent-strong">{c.marker}</sup>{" "}
                          <button
                            type="button"
                            onClick={() => setViewing(c)}
                            className="border-b border-transparent font-mono text-[13px] text-muted hover:border-accent hover:text-ink"
                          >
                            {forgeLine(c)}
                          </button>
                        </li>
                      ))}
                    </ul>
                  </details>
                )}

                {/* Re-explaining needs a stored answer to build from — never on a
                    turn that failed or ended by asking. */}
                {turn.done && turn.messageId && !turn.error && !turn.clarification && (
                  <div className="mt-4 flex items-center gap-2">
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => reexplain(i)}
                      className="rounded-full border border-border bg-panel px-3.5 py-1.5 text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active disabled:opacity-50"
                    >
                      {turn.audience === "dev" ? "Explain as Analyst" : "Explain as Developer"}
                    </button>
                    <button
                      type="button"
                      onClick={() => copy(i)}
                      className="rounded-full border border-border bg-panel px-3.5 py-1.5 text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active"
                    >
                      {copied === i ? "Copied" : "Copy as Markdown"}
                    </button>
                    {turn.tokens > 0 && (
                      <span className={pill + " ml-auto bg-active font-mono text-faint"}>
                        {turn.tokens.toLocaleString("en-GB")} tokens
                      </span>
                    )}
                  </div>
                )}
              </article>
            ))}
            <div ref={bottom} />
          </div>
        </div>

        <form
          onSubmit={submit}
          className="max-w-[900px] bg-[linear-gradient(to_bottom,transparent,var(--color-bg)_30%)] px-10 pt-3 pb-4"
        >
          {/* The question gets the whole width; the controls sit under it in
              their own row, so a long question and its settings never fight
              for the same line. */}
          <div className="rounded-ui-lg border border-border bg-panel px-3 pt-2 pb-2 shadow-panel focus-within:border-accent focus-within:ring-2 focus-within:ring-accent-dim">
            <textarea
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  void submit();
                }
              }}
              ref={box}
              rows={1}
              aria-label="Question"
              placeholder="Ask about the code…"
              className="block w-full resize-none bg-transparent px-1 py-2 text-[15px] text-ink outline-none"
            />
            <div className="mt-1.5 flex items-center gap-2">
              <fieldset className="inline-flex gap-0.5 rounded-full border border-border bg-bg p-0.5" aria-label="Role">
                {(["ba", "dev"] as const).map((role) => (
                  <button
                    key={role}
                    type="button"
                    aria-pressed={audience === role}
                    onClick={() => setAudience(role)}
                    className={
                      "rounded-full px-3 py-1 text-xs font-medium " +
                      (audience === role ? "bg-accent-fill text-ink" : "text-muted hover:text-ink")
                    }
                  >
                    {roleName(role)}
                  </button>
                ))}
              </fieldset>
              <label className="relative inline-flex h-8 items-center rounded-full border border-border bg-bg pr-2.5 pl-3 text-xs text-muted hover:border-elevated-border hover:text-ink">
                <span className="sr-only">Answer language</span>
                <select
                  aria-label="Answer language"
                  value={language}
                  onChange={(e) => setLanguage(e.target.value)}
                  className="lang-select cursor-pointer border-0 bg-transparent pr-4 text-inherit outline-none"
                >
                  {languages.map((l) => (
                    <option key={l.code} value={l.code}>
                      {l.name}
                    </option>
                  ))}
                </select>
                <span className="pointer-events-none absolute right-2 rotate-90">
                  <Chevron />
                </span>
              </label>
              <span className="ml-auto hidden text-xs text-faint sm:inline">Shift+Enter for a new line</span>
              <button
                type="submit"
                disabled={busy}
                className="rounded-full bg-accent-fill px-4.5 py-1.5 text-sm font-medium text-ink hover:bg-accent-strong disabled:opacity-50"
              >
                Ask
              </button>
            </div>
          </div>
        </form>
      </div>

      <aside aria-label="Sources" className="hidden min-h-0 flex-col border-l border-border bg-panel xl:flex">
        <header className="flex items-center border-b border-border px-4.5 py-3.5 text-[11px] font-medium uppercase tracking-[.12em] text-faint">
          Sources
          {sourceTurn && (
            <span className="ml-auto font-mono tracking-normal">
              turn {sourceTurnIndex + 1} · {sourceTurn.citations.length}
            </span>
          )}
        </header>
        <div className="min-h-0 flex-1 overflow-auto">
          {!sourceTurn && (
            <p className="px-4.5 py-4 text-[13px] text-faint">
              The files an answer was written from appear here, numbered like the markers in the text.
            </p>
          )}
          {sourceTurn?.citations.map((c) => (
            // The whole row opens the file; the file name underlines on
            // hover so the row reads as something to open, without a glyph.
            <button
              key={c.marker}
              type="button"
              onClick={() => setViewing(c)}
              className={
                "group grid w-full grid-cols-[26px_1fr] gap-x-2 gap-y-0.5 border-b border-border-soft px-4.5 py-3.5 text-left text-[13px] " +
                (hot === c.marker ? "bg-active" : "hover:bg-active")
              }
            >
              <span className="row-span-2 font-mono font-semibold text-accent-strong">{c.marker}</span>
              <span className="font-medium text-ink">
                {c.repo}
                <span className="ml-1.5 font-mono text-[11.5px] font-normal text-faint">{c.branch}</span>
              </span>
              <span className="font-mono text-xs break-all text-muted">
                {c.path.includes("/") ? c.path.slice(0, c.path.lastIndexOf("/") + 1) : ""}
                <b className="font-medium text-ink-dim underline-offset-[3px] group-hover:underline group-hover:decoration-accent">
                  {c.path.slice(c.path.lastIndexOf("/") + 1)}
                </b>
                :{c.start_line}-{c.end_line}
              </span>
            </button>
          ))}
        </div>
      </aside>

      {viewing && <SourceView source={viewing} onClose={() => setViewing(null)} />}
    </div>
  );
}
