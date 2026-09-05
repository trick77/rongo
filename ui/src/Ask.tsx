import { useEffect, useRef, useState } from "react";
import Markdown from "./markdown";
import Clarify, { type ClarifyCandidate } from "./Clarify";
import Question from "./Question";
import Trace, { type Step, type TraceState } from "./Trace";
import { Chevron } from "./icons";
import SourceView, { type SourceRef } from "./SourceView";
import { mermaidize } from "./diagramExport";

type Citation = SourceRef;

type Audience = "ba" | "dev";

/** The languages the answer can be written in; the backend's allowlist. */
export const languages: { code: string; name: string }[] = [
  { code: "en", name: "English" },
  { code: "de", name: "Deutsch" },
  { code: "fr", name: "Français" },
  { code: "it", name: "Italiano" },
];

/** What the empty page and the composer say, in the language the select is
 * set to: the two pieces of chrome a reader meets before any answer, so they
 * follow the answer language too. The rest of the chrome stays English.
 *
 * The placeholder belongs here rather than beside the textarea because it is
 * the same invitation as the title, in the place the answer is asked for.
 *
 * The role names are NOT translated: they are the labels on the controls
 * next to this text, and a body naming "un Analyste" beside a button
 * reading "Analyst" points at something that is not on the page. */
const welcome: Record<string, { title: string; body: string; placeholder: string }> = {
  en: {
    title: "Ask about the code.",
    placeholder: "Ask about the code…",
    body:
      "rongo searches the indexed repositories, asks back when a question fits more than one " +
      "mechanism, and answers with sources for every claim. Pick a role: an Analyst gets the " +
      "mechanism in domain terms, a Developer gets types, functions and files.",
  },
  de: {
    title: "Frag den Code.",
    placeholder: "Frag den Code …",
    body:
      "rongo durchsucht die indexierten Repositories, fragt nach, wenn eine Frage auf mehr als einen " +
      "Mechanismus passt, und antwortet mit Quellen für jede Aussage. Wähl eine Rolle: Ein Analyst " +
      "bekommt den Mechanismus in Fachbegriffen, ein Developer Typen, Funktionen und Dateien.",
  },
  fr: {
    title: "Interrogez le code.",
    placeholder: "Interrogez le code …",
    body:
      "rongo parcourt les dépôts indexés, pose une question en retour quand la vôtre correspond à " +
      "plus d'un mécanisme, et répond avec des sources pour chaque affirmation. Choisissez un rôle : " +
      "un Analyst reçoit le mécanisme dans les termes du métier, un Developer les types, les " +
      "fonctions et les fichiers.",
  },
  it: {
    title: "Chiedi al codice.",
    placeholder: "Chiedi al codice …",
    body:
      "rongo cerca nei repository indicizzati, chiede chiarimenti quando una domanda corrisponde a " +
      "più di un meccanismo e risponde con fonti per ogni affermazione. Scegli un ruolo: un Analyst " +
      "riceve il meccanismo nei termini del dominio, un Developer tipi, funzioni e file.",
  },
};

/** The clarification a turn ended with, as this view needs it: the id of the
 * message that carries the card (used to resume it) and its candidates. */
type TurnClarification = {
  messageId: number;
  candidates: ClarifyCandidate[];
};

/** What a failed turn is asked again with: the endpoint and the body of the
 * request that failed, kept so a retry re-issues exactly that. Null on a turn
 * that has nothing to offer — one still running, one that answered, and a
 * resume, whose card is its own retry. */
type RetryRequest = {
  url: string;
  body: Record<string, unknown>;
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
  // The request this turn was made with, kept so a failure can be asked
  // again. Set while the turn streams, so it is there whichever way the turn
  // ends; only a turn that also carries an error ever shows the button.
  retry: RetryRequest | null;
  done: boolean;
  // The id of the message this turn was recorded as, once known. Reexplain
  // needs it; a turn still in flight has none yet.
  messageId: number | null;
  clarification: TurnClarification | null;
  // What the turn had to say about its own scope - a repository the question
  // named that the index does not carry. Empty on every ordinary turn, and
  // shown above the answer rather than inside it: it is about the answer, not
  // part of it.
  notice: string;
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
  // What the turn paid for — every call it made, summed, and priced when
  // the server has prices. Null until the usage event arrives, and for a
  // stored turn older than the record of it.
  usage: Usage | null;
  // The moment the question was asked, as the record has it, or now.
  askedAt: string;
  // The questions this answer offers to ask next. Only the newest answered
  // turn shows them: the older ones are a record of what was asked, and a
  // thread of stale offers would compete with the answer in front of the
  // reader.
  followups: string[];
};

/** One paid call of a turn, as the usage event and the record carry it. */
export type UsageCall = {
  step: string;
  model: string;
  prompt_tokens: number;
  completion_tokens: number;
  cost_usd?: number;
};

/** Usage is the usage event's payload and a stored message's `usage`. */
export type Usage = {
  calls: UsageCall[];
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  // Present as soon as the server has any price configured, absent when it
  // has none. Absent and zero are different things: zero is "priced, and it
  // cost nothing", absent is "not priced here".
  cost_usd?: number;
};

/** tokens formats a count the way the pill shows it. */
function tokens(n: number): string {
  return n.toLocaleString("en-GB") + " tok";
}

/** money formats a USD figure at the resolution a turn costs: a turn is
 * fractions of a cent, a thread whole cents, so three decimals until a
 * dollar and two beyond. */
export function money(usd: number): string {
  return "$" + (usd >= 1 ? usd.toFixed(2) : usd.toFixed(3));
}

/** threadUsage sums every turn that has usage. Cost is a number only when
 * some turn carried one, so a thread on an unpriced server shows no money. */
export function threadUsage(turns: Turn[]): { tokens: number; cost: number | null } | null {
  let total = 0;
  let cost: number | null = null;
  let any = false;
  for (const t of turns) {
    if (!t.usage) continue;
    any = true;
    total += t.usage.total_tokens;
    if (t.usage.cost_usd != null) cost = (cost ?? 0) + t.usage.cost_usd;
  }
  return any ? { tokens: total, cost } : null;
}

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
  // The scope sentence, rendered by the backend in this message's own
  // language. Empty on every turn that named nothing the index lacked.
  notice?: string;
  clarification: { id: number; candidates: ClarifyCandidate[] } | null;
  from_candidate_idx: number;
  // The clarification this message resolved, or 0 when it did not resume
  // one. The link the backend actually stored — matching on it, not on
  // position, is what tells two clarifications open in the same thread
  // apart.
  from_clarification_id: number;
  created_at?: string;
  // What this answer offered to ask next. Absent on a turn that ended in a
  // card, a failure or a nothing-found, and on every turn older than the
  // column.
  followups?: string[] | null;
  // Absent for a turn with nothing on record: older than the usage table,
  // or one that paid for nothing.
  usage?: Usage | null;
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
    notice: m.notice ?? "",
    steps: [],
    error: m.error ?? "",
    // Filled in by storedRetries, which needs the turn's neighbours.
    retry: null,
    done: true,
    messageId: m.id,
    clarification: m.clarification ? { messageId: m.id, candidates: m.clarification.candidates } : null,
    chosenIdx: null,
    live: false,
    startedAt: 0,
    endedAt: 0,
    usage: m.usage ?? null,
    askedAt: m.created_at ?? "",
    followups: m.followups ?? [],
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
    notice: "",
    steps: [],
    error: "",
    retry: null,
    done: false,
    messageId: null,
    clarification: null,
    chosenIdx: null,
    live: true,
    startedAt: now,
    endedAt: null,
    usage: null,
    askedAt: new Date(now).toISOString(),
    followups: [],
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

/**
 * Gives every failed turn in a restored thread the request that asks it
 * again. The record keeps no clarification link on a turn that failed — the
 * backend writes it only when the answer lands — and no re-explain source
 * either, so a stored failure is only ever re-run as a fresh question. That is
 * what a correction is anyway.
 *
 * The exception keeps the resume rule holding across a reload: a failed turn
 * below a card nobody has answered, on the same question, IS that card's
 * resume. Its retry is the card, which is still open, so it gets no request
 * and no button.
 *
 * The walk back skips the failures in between, because a card can be tried
 * more than once: pick, fail, pick again, fail again leaves TWO failed turns
 * under the one still-open card, and reading only the turn directly above
 * would give the second one a button that posts a fresh question and opens a
 * second card beside the first.
 *
 * The record cannot tell that resume from a reader who ignored the card and
 * typed the same question again — nothing is stored that separates them — so
 * this reads both as the resume and leaves them to the card. The rarer case
 * loses a button and keeps the card and the composer; the other way round
 * would put two ways of spending the same resume on screen.
 */
function storedRetries(turns: Turn[]): Turn[] {
  return turns.map((t, i) => {
    if (!t.error) return t;
    let j = i - 1;
    while (j >= 0 && turns[j].error && !turns[j].clarification && turns[j].question === t.question) j--;
    const card = j >= 0 ? turns[j] : null;
    if (card && card.clarification && card.chosenIdx == null && card.question === t.question) {
      return t;
    }
    return {
      ...t,
      retry: { url: "/api/ask", body: { question: t.question, audience: t.audience, language: t.language } },
    };
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

/** asMarkdown is what "Copy as Markdown" puts on the clipboard: the answer
 * with its markers, and the sources they point at.
 *
 * A diagram travels as mermaid rather than as the `diagram` fence it is
 * written in. The fence is rongo's own shape and draws nowhere else, so a
 * pasted answer used to carry a block of JSON where its picture had been. */
function asMarkdown(turn: Turn): string {
  const lines = [`# ${turn.question}`, "", mermaidize(turn.text.trim())];
  if (turn.citations.length > 0) {
    lines.push("", "Sources:", ...turn.citations.map((c) => `[${c.marker}] ${forgeLine(c)}`));
  }
  return lines.join("\n") + "\n";
}

const pill = "rounded-full px-2.5 py-0.5 text-xs whitespace-nowrap";

/**
 * The composer's answer language, remembered across reloads — someone who
 * works in German re-picked it on every reload otherwise. Only the default for
 * a NEW thread: once a thread has a turn it is answered in that turn's
 * language, and the select shows it without offering a change.
 *
 * Guarded like App's thread bookmark, because Safari's private mode throws on
 * storage access, and a blank page is worse than a forgotten preference. A
 * code outside the backend's allowlist falls back to English rather than
 * sending the server something it will reject.
 */
const langKey = "rongo.language";

function storedLanguage(): string {
  try {
    const raw = localStorage.getItem(langKey);
    return raw && languages.some((l) => l.code === raw) ? raw : "en";
  } catch {
    return "en";
  }
}

function rememberLanguage(code: string) {
  try {
    localStorage.setItem(langKey, code);
  } catch {
    // See storedLanguage.
  }
}

export default function Ask({
  threadId: openThread = null,
  onThread = () => {},
  onActivity = () => {},
  onBusy = () => {},
  onUsage = () => {},
}: {
  /** The thread to show, or null for a fresh one. */
  threadId?: number | null;
  /** Reports the thread this view is on; null means the id led nowhere. */
  onThread?: (id: number | null) => void;
  /** Something changed that the thread list should see. */
  onActivity?: () => void;
  /** Reports whether a turn is in flight, so the thread list can lock. */
  onBusy?: (busy: boolean) => void;
  /** Reports the thread's running total — every turn on screen summed, the
   * ones that asked back or failed included — or null when nothing is
   * known yet. The header shows it next to the title. */
  onUsage?: (total: { tokens: number; cost: number | null } | null) => void;
}) {
  const [question, setQuestion] = useState("");
  const [audience, setAudience] = useState<Audience>("ba");
  const [language, setLanguage] = useState(storedLanguage);
  const [turns, setTurns] = useState<Turn[]>([]);
  // Whether a thread's turns are on their way. Distinct from busy, which is a
  // turn being answered: this is the record being fetched, and it is what
  // tells the empty column to hold the shape of a thread instead of offering
  // the welcome to a reader who has just opened a conversation.
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  // The marker under the pointer, so the Sources pane can point back.
  const [hot, setHot] = useState<number | null>(null);
  // The source open in the viewer, or null while it is closed.
  const [viewing, setViewing] = useState<Citation | null>(null);
  const [copied, setCopied] = useState<number | null>(null);
  // The turn whose usage breakdown is open, if any. One at a time: it is a
  // glance at what a turn cost, not a report to keep open.
  const [openUsage, setOpenUsage] = useState<number | null>(null);
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
  // The scrolling column, and whether the view is currently following what
  // arrives into it. It follows while the reader sits at the foot of the
  // thread, stops the moment they scroll up to read something back, and
  // follows again once they return to the bottom.
  const view = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  // Set when the turns on screen come from opening a thread rather than from
  // asking. A record is read from its beginning: the question and the answer
  // it was opened for are at the top, so the view lands there and stays until
  // the reader moves it.
  const opened = useRef(false);

  // Markdown is memoized, and a fresh arrow per render would defeat it on
  // every turn at once. One handler per turn index is kept instead, and it
  // reads the turns through a ref so it never goes stale: the index is the
  // position in the thread on screen, which is what the reader clicked in.
  const live = useRef(turns);
  live.current = turns;
  const markerOpen = useRef(new Map<number, (marker: number) => void>());
  function openMarker(i: number) {
    let f = markerOpen.current.get(i);
    if (!f) {
      f = (marker: number) => {
        const c = live.current[i]?.citations.find((x) => x.marker === marker);
        if (c) setViewing(c);
      };
      markerOpen.current.set(i, f);
    }
    return f;
  }

  // The same for the set of backed markers: it is derived from a turn's
  // citations, so it is cached against that very array and only rebuilt when
  // the citations themselves are replaced.
  const backedSets = useRef(new WeakMap<Citation[], Set<number>>());
  function backedMarkers(turn: Turn) {
    if (!turn.done) return undefined;
    let s = backedSets.current.get(turn.citations);
    if (!s) {
      s = new Set(turn.citations.map((c) => c.marker));
      backedSets.current.set(turn.citations, s);
    }
    return s;
  }

  // Retires a thread load still in the air, with everything that was waiting
  // on it. The record must not land on top of what the reader is doing — and
  // the skeleton would otherwise sit above their new turn for good, with the
  // scroll effects still sitting the load out, so the answer they are watching
  // would write itself off the bottom edge.
  function retireLoad() {
    loadSeq.current++;
    setLoading(false);
    opened.current = false;
  }

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
    // A breakdown is a turn index into the thread that is being left; the
    // same index in the next thread is a different turn. And the total in
    // the header belongs to the old thread until the new one has loaded.
    setOpenUsage(null);
    onUsage(null);
    // The body empties in the same commit as the header, not a beat behind
    // it: the conversation that was just left used to stand under the new
    // thread's title for as long as the load took, which is the whole of the
    // stagger a reader sees when switching threads. The turns go now and the
    // skeleton holds their place — an empty list alone would offer the
    // welcome to someone who has just opened a thread.
    setTurns([]);
    opened.current = true;
    if (openThread === null) {
      setLoading(false);
      opened.current = false;
      return;
    }
    setLoading(true);
    // The whole arrival in one commit: the turns, the header's running total
    // and the end of the skeleton. Leaving the total to the effect that
    // watches `turns` painted the thread first and the total a frame later,
    // which is the last of the steps a reader could see.
    const arrive = (next: Turn[]) => {
      setTurns(next);
      setLoading(false);
      onUsage(threadUsage(next));
    };
    (async () => {
      try {
        const res = await fetch(`/api/threads/${openThread}`);
        if (!res.ok) {
          // 503, 500 and 401 mean "not right now", not "not yours". Treating
          // them as a dead thread would drop the bookmark on a passing blip and
          // the conversation would be gone on the next reload.
          if (seq === loadSeq.current) arrive([]);
          return;
        }
        const list = await res.json();
        if (seq !== loadSeq.current) return;
        // A thread that is not yours, or no longer exists, comes back as an
        // empty list with status 200 — the owner check sits inside the query.
        // Rendering that as an empty thread would keep a dead id around
        // forever, so it is handed back as "no thread".
        if (!Array.isArray(list) || list.length === 0) {
          arrive([]);
          shown.current = null;
          threadId.current = null;
          onThread(null);
          return;
        }
        arrive(storedRetries(linkChosenCandidates(list, list.map(storedTurn))));
      } catch {
        // The turns are cleared rather than left standing: the ids have already
        // moved to the new thread, and a visible conversation that belongs to
        // the old one would send the next question somewhere the reader is not
        // looking.
        if (seq === loadSeq.current) arrive([]);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openThread]);

  // A new turn puts the reader at the foot of the thread — that is where the
  // answer will appear. A thread that was just OPENED is exempt: its turns
  // arrive in one go and belong to the top of the view, not the bottom.
  useEffect(() => {
    if (opened.current) return;
    following.current = true;
    bottom.current?.scrollIntoView?.({ block: "end" });
  }, [turns.length]);

  // The answer grows downwards while it streams, so the view follows it —
  // otherwise the reader watches an answer write itself off the bottom edge
  // and has to chase it. Every token patches the turn, so this runs on each
  // one; it does nothing while the reader is reading further up. A thread
  // that has just loaded lands here too, and goes to its first line instead:
  // it is a record being read, not an answer being written.
  useEffect(() => {
    const el = view.current;
    if (!el) return;
    // The emptying that precedes a load is not an arrival: consuming
    // `opened` here would spend it on the skeleton, and the turns that follow
    // would then be taken for a new question and scrolled to the foot.
    if (loading) return;
    if (opened.current) {
      opened.current = false;
      following.current = false;
      el.scrollTop = 0;
      return;
    }
    if (following.current) el.scrollTop = el.scrollHeight;
  }, [turns]);

  // The running total follows the turns: it grows when a usage event lands
  // and resets when another thread is opened.
  useEffect(() => {
    onUsage(threadUsage(turns));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turns]);

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
   *
   * Returns whether the turn produced an answer. A caller that changed
   * something on an earlier turn before streaming — picking a candidate —
   * needs that to undo it: a turn that failed left the server's record
   * untouched.
   */
  async function stream(url: string, body: Record<string, unknown>, retryable = true): Promise<boolean> {
    markBusy(true);
    let ok = true;
    // Terminal events, the two ways a turn is meant to end. A stream that
    // closes without one of them — a proxy FIN, a backend restart mid-answer —
    // used to leave the turn running forever: the loop below simply exits and
    // nothing patched the turn, so the trace ticked on with no answer, no
    // error and no way out.
    let closed = false;
    // The request the turn was made with, so a failure can be asked again. A
    // resume passes retryable false: its card unlocks itself on failure, and a
    // button beside the error would be a second way to spend the same call.
    if (retryable) patchLast((t) => ({ ...t, retry: { url, body } }));
    try {
      const res = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok || !res.body) {
        patchLast((t) => ({ ...t, error: `The server answered with ${res.status}.`, done: true, endedAt: Date.now() }));
        ok = false;
        return false;
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
          } else if (name === "title") {
            // The model's title, the moment it exists rather than at the end
            // of the turn. The text itself is not kept here: the rail and the
            // header read the list, so refreshing it is the whole update.
            onActivity();
          } else if (name === "status") {
            patchLast((t) => ({ ...t, steps: [...t.steps, { step: payload.step, at: Date.now() }] }));
          } else if (name === "notice") patchLast((t) => ({ ...t, notice: payload.text ?? "" }));
          else if (name === "token") patchLast((t) => ({ ...t, text: t.text + payload.text }));
          else if (name === "citations") patchLast((t) => ({ ...t, citations: payload ?? [] }));
          else if (name === "followups") patchLast((t) => ({ ...t, followups: payload ?? [] }));
          else if (name === "usage") patchLast((t) => ({ ...t, usage: payload as Usage }));
          else if (name === "clarification") {
            patchLast((t) => ({
              ...t,
              messageId: payload.message_id,
              clarification: { messageId: payload.message_id, candidates: payload.candidates ?? [] },
            }));
          } else if (name === "error") {
            ok = false;
            closed = true;
            patchLast((t) => ({ ...t, error: payload.message, done: true, endedAt: Date.now() }));
          }
          else if (name === "done") {
            closed = true;
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
      // The stream ended without saying how. From the reader's side that is
      // the same thing as losing the connection, so it reads the same.
      if (!closed) {
        ok = false;
        patchLast((t) => ({ ...t, error: "The connection was lost.", done: true, endedAt: Date.now() }));
      }
    } catch {
      ok = false;
      patchLast((t) => ({ ...t, error: "The connection was lost.", done: true, endedAt: Date.now() }));
    } finally {
      markBusy(false);
    }
    return ok;
  }

  /**
   * A thread is answered in one language: the one its first question was asked
   * in. Everything else in the thread already works that way — the title is
   * written once from the first turn, and follow-up pills, re-explains, retries
   * and resumed cards all replay the language of the turn they continue — so a
   * mid-thread switch produced a German answer under an English title beside
   * English suggestions. The backend pins the same way when the record is
   * written; this is what keeps the composer from promising otherwise.
   *
   * Null on a thread with no turn yet, which is where the remembered
   * preference still applies.
   */
  const threadLanguage = turns.length > 0 ? turns[0].language : null;
  // What the next question will actually be answered in.
  const asking = threadLanguage ?? language;

  async function submit(e?: React.FormEvent) {
    e?.preventDefault();
    const q = question.trim();
    if (!q || busy) return;

    // A thread load still in the air would replace the whole turn list when it
    // lands, dropping the turn appended just below — and every token after that
    // would be patched into the last STORED answer instead, corrupting a
    // finished turn on screen while this question disappeared. Retiring the
    // load is what the reader expects anyway: they have moved on.
    retireLoad();

    setTurns((prev) => [...prev, freshTurn(q, audience, asking)]);
    setQuestion("");

    await stream("/api/ask", { question: q, audience, language: asking, thread_id: threadId.current ?? 0 });
  }

  /**
   * A choice on a clarification card does two things: it marks the card that
   * asked, and it starts a NEW turn for the answer. Nothing about the card is
   * overwritten, but it is answered once: a card that already carries a choice
   * takes no further one, and the backend refuses a second resume with 409.
   *
   * The mark is put on before the answer arrives, so the ochre goes the moment
   * the reader decides. A turn that fails takes it back off: the server records
   * the choice only when the answer lands, so a card left locked here would
   * strand the reader with no way to retry.
   */
  async function chooseCandidate(turnIndex: number, idx: number) {
    if (busy) return;
    const turn = turns[turnIndex];
    if (!turn.clarification || turn.chosenIdx != null) return;

    retireLoad();
    setTurns((prev) => [
      ...prev.map((t, i) => (i === turnIndex ? { ...t, chosenIdx: idx } : t)),
      freshTurn(turn.question, turn.audience, turn.language),
    ]);

    const ok = await stream("/api/ask", {
      thread_id: threadId.current ?? 0,
      question: turn.question,
      audience: turn.audience,
      language: turn.language,
      clarification_message_id: turn.clarification.messageId,
      choice: idx,
    }, false);
    if (!ok) setTurns((prev) => prev.map((t, i) => (i === turnIndex ? { ...t, chosenIdx: null } : t)));
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

    retireLoad();
    setTurns((prev) => [...prev, freshTurn(turn.question, nextAudience, turn.language)]);

    await stream(`/api/messages/${turn.messageId}/reexplain`, { audience: nextAudience });
  }

  /**
   * A suggested question is asked like any other, in this thread. It carries
   * the ANSWERING turn's role and language, not whatever the composer is set
   * to now: the pill was written for that reader in that language, and a
   * German suggestion answered in English reads as a different product.
   *
   * The thread is a record — this appends a turn, and the answer the pill
   * came from is left exactly as it was.
   */
  async function askFollowup(turnIndex: number, question: string) {
    if (busy) return;
    const turn = turns[turnIndex];

    retireLoad();
    setTurns((prev) => [...prev, freshTurn(question, turn.audience, turn.language)]);

    await stream("/api/ask", {
      question,
      audience: turn.audience,
      language: turn.language,
      thread_id: threadId.current ?? 0,
    });
  }

  /**
   * Asks a failed turn again. Like every other action here it is a NEW turn
   * appended at the end, never a rewrite: the failed one stays in the thread
   * with its error and what it cost. The reader starts it; nothing re-runs on
   * its own.
   */
  async function retry(turnIndex: number) {
    if (busy) return;
    const turn = turns[turnIndex];
    const req = turn.retry;
    if (!req) return;

    retireLoad();
    setTurns((prev) => [...prev, freshTurn(turn.question, turn.audience, turn.language)]);

    // The thread id is taken now, not from the stored body: the thread may
    // have been created by the very turn that failed, and a stored turn's
    // request carries none at all.
    await stream(
      req.url,
      req.url === "/api/ask" ? { ...req.body, thread_id: threadId.current ?? 0 } : req.body,
    );
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
    // below that (every iPad in portrait, the 11" in landscape) the thread
    // has the width, the chips in the text open the sources, and the
    // per-answer details block still lists them.
    <div className="grid h-full min-h-0 grid-cols-1 xl:grid-cols-[1fr_300px] 2xl:grid-cols-[1fr_340px]">
      <div className="relative flex min-h-0 min-w-0 flex-col">
        {busy && <div className="busybar" aria-hidden="true" />}
        <div
          ref={view}
          // Wheel, trackpad, touch and the keyboard all arrive here, so one
          // handler covers every way of leaving the bottom. The scrolls this
          // view makes itself land at the bottom and keep it following — bar
          // the jump to the top when a thread is opened, which is a reader
          // sitting at the head of a record, not following anything.
          onScroll={() => {
            const el = view.current;
            if (el) following.current = el.scrollHeight - el.scrollTop - el.clientHeight <= 48;
          }}
          className="min-h-0 flex-1 overflow-auto"
        >
          <div className="max-w-[900px] px-4 pt-5 pb-8 sm:px-6 lg:px-10 lg:pt-8 lg:pb-10 [@media(max-height:500px)]:pt-3">
            {/* No top margin on the welcome: it starts where the Repositories
                heading starts, both pages' first line on the same rule. */}
            {turns.length === 0 && !loading && (
              <div className="max-w-[52ch]">
                <h2 className="font-serif text-[22px] font-medium leading-tight tracking-tight text-ink sm:text-[28px]">
                  {(welcome[asking] ?? welcome.en).title}
                </h2>
                <p className="mt-3 text-muted">{(welcome[asking] ?? welcome.en).body}</p>
              </div>
            )}

            {/* The thread that is being fetched, in outline: the eyebrow, the
                question and a few lines of answer, in the places they will
                land in. It is the shape of a turn, not a spinner — the column
                does not move again when the text replaces it. */}
            {loading && (
              // role="status": a bare div may carry no accessible name, so a
              // reader who does not see the shape would be told nothing at all
              // while the thread is on its way.
              <div className="max-w-[68ch]" role="status" aria-busy="true" aria-label="Opening the thread">
                <div className="skeleton h-3 w-[7ch] rounded-ui-sm" />
                <div className="skeleton mt-2.5 h-7 w-[24ch] rounded-ui-sm" />
                <div className="mt-4 flex gap-1.5">
                  <div className="skeleton h-5 w-[8ch] rounded-full" />
                  <div className="skeleton h-5 w-[6ch] rounded-full" />
                </div>
                <div className="mt-6 space-y-2.5">
                  <div className="skeleton h-3.5 w-full rounded-ui-sm" />
                  <div className="skeleton h-3.5 w-full rounded-ui-sm" />
                  <div className="skeleton h-3.5 w-[86%] rounded-ui-sm" />
                  <div className="skeleton h-3.5 w-[62%] rounded-ui-sm" />
                </div>
              </div>
            )}

            {turns.map((turn, i) => (
              <article
                key={i}
                className="mb-8 border-b border-border-soft pb-8 last:mb-0 last:border-b-0 [@media(max-height:500px)]:mb-4 [@media(max-height:500px)]:pb-4"
              >
                <div className="text-[11px] font-medium uppercase tracking-[.1em] text-accent-strong">
                  {roleName(turn.audience)}
                </div>
                {/* The accent is the eyebrow's alone now: the question is what
                    was typed, at whatever length it was typed, and it reads as
                    the reader's words rather than as a headline. */}
                <Question text={turn.question} />
                <div className="mt-2.5 flex items-center gap-1.5">
                  {turn.askedAt && <time className="font-mono text-[11.5px] text-faint">{clock(turn.askedAt)}</time>}
                  <span className={pill + " bg-active text-muted"}>Turn {i + 1}</span>
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

                {/* Above the answer, and not ochre: ochre means "your move",
                    and there is no move to make here - the turn already did
                    what it could and is saying what it could not. */}
                {turn.notice && (
                  <div
                    role="note"
                    className="mt-4 flex max-w-[68ch] items-start gap-2.5 rounded-ui-sm border border-border border-l-2 border-l-elevated-border bg-panel px-3.5 py-2.5"
                  >
                    <span aria-hidden="true" className="font-mono text-muted">
                      !
                    </span>
                    <p className="m-0 text-[13.5px] text-muted">{turn.notice}</p>
                  </div>
                )}

                {turn.clarification && (
                  <Clarify
                    candidates={turn.clarification.candidates}
                    chosenIdx={turn.chosenIdx}
                    onChoose={(idx) => chooseCandidate(i, idx)}
                  />
                )}

                {/* ui-markdown carries the prose typography (index.css), the
                    same block ../loom uses. The measure stays capped here:
                    rongo's answer column is wider than loom's rail.

                    streaming draws the caret (index.css) on the answer's last
                    block rather than after the container: as a sibling of the
                    markdown the caret was a block of its own, and blinked on
                    the line below the words it belongs to. */}
                {turn.text && (
                  <div
                    className={`ui-markdown mt-4 max-w-[68ch]${turn.done ? "" : " streaming"}`}
                  >
                    <Markdown
                      text={turn.text}
                      onMarkerHover={i === sourceTurnIndex ? setHot : undefined}
                      // Every turn, from its own list: the pane shows only
                      // the newest, and a tablet has no pane at all.
                      onMarkerOpen={openMarker(i)}
                      // Known once the turn is done: the citations event is
                      // the last thing before done, so a finished turn with
                      // none has none.
                      backed={backedMarkers(turn)}
                      // Only a turn of THIS session fades its text in: it is
                      // the one whose words are arriving. A stored thread is
                      // mounted whole, and fading it would replay a
                      // conversation that was written long ago.
                      //
                      // Kept on for the rest of the turn's life rather than
                      // dropped at `done`: turning it off unwraps the
                      // segments, and the last ones - younger than the fade
                      // itself - would snap to full brightness at the moment
                      // the answer ends. Nothing re-fades, because the
                      // segments keep their keys and never remount.
                      fade={turn.live}
                    />
                  </div>
                )}

                {/* The retry sits beside the error, not in the footer below:
                    the footer only renders for a turn that has usage, and a
                    turn whose first call never reached the upstream has none.
                    It stays for the life of the turn — a second click is a
                    third turn, which is honest, and a "already retried" flag
                    would not survive a reload anyway. */}
                {turn.error && (
                  <div className="mt-3 flex flex-wrap items-center gap-3">
                    <p role="alert" className="m-0 text-accent-strong">
                      {turn.error}
                    </p>
                    {turn.retry && (
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => retry(i)}
                        className="rounded-full border border-border bg-panel px-3.5 py-1.5 text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active disabled:opacity-50"
                      >
                        Retry
                      </button>
                    )}
                  </div>
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

                {/* What to ask next, under the answer that prompted it and
                    above the actions on it. Only on the newest turn: an older
                    answer's offers are spent, and a card or a failed turn
                    below means the reader's move is there, not here.

                    Never ochre — ochre is "your move", and nothing here is
                    waiting on the reader. */}
                {i === turns.length - 1 && turn.done && turn.followups.length > 0 && (
                  <nav aria-label="Follow-up questions" className="mt-4 flex flex-wrap gap-2">
                    {turn.followups.map((q) => (
                      <button
                        key={q}
                        type="button"
                        disabled={busy}
                        onClick={() => askFollowup(i, q)}
                        className="rounded-full border border-border bg-panel px-3.5 py-1.5 text-left text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active disabled:opacity-50"
                      >
                        {q}
                      </button>
                    ))}
                  </nav>
                )}

                {/* The footer: the two actions need a stored answer to build
                    from — never on a turn that failed or ended by asking. The
                    usage pill does not: a turn that asked back or failed still
                    paid for its gates, and the thread total counts them. */}
                {turn.done && (turn.usage || (turn.messageId && !turn.error && !turn.clarification)) && (
                  <div className="mt-4">
                    <div className="flex items-center gap-2">
                      {turn.messageId && !turn.error && !turn.clarification && (
                        <>
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
                        </>
                      )}
                      {turn.usage && (
                        <button
                          type="button"
                          aria-expanded={openUsage === i}
                          aria-label={`Usage of turn ${i + 1}`}
                          onClick={() => setOpenUsage(openUsage === i ? null : i)}
                          className={
                            pill +
                            " ml-auto inline-flex items-center gap-1.5 font-mono " +
                            (openUsage === i ? "bg-elevated text-muted" : "bg-active text-faint hover:text-muted")
                          }
                        >
                          {tokens(turn.usage.total_tokens)}
                          {turn.usage.cost_usd != null && (
                            <>
                              <span className="opacity-50">·</span>
                              {money(turn.usage.cost_usd)}
                            </>
                          )}
                          <Chevron open={openUsage === i} />
                        </button>
                      )}
                    </div>
                    {turn.usage && openUsage === i && (
                      <div className="mt-2.5 ml-auto w-full max-w-[470px] overflow-x-auto rounded-ui border border-border bg-panel px-3.5 py-2.5 font-mono text-xs">
                        <table className="w-full border-collapse">
                          <thead>
                            <tr className="text-faint">
                              <th className="border-b border-border-soft pb-1.5 text-left font-normal">call</th>
                              <th className="border-b border-border-soft pb-1.5 text-right font-normal">in</th>
                              <th className="border-b border-border-soft pb-1.5 text-right font-normal">out</th>
                              {turn.usage.cost_usd != null && (
                                <th className="border-b border-border-soft pb-1.5 text-right font-normal">cost</th>
                              )}
                            </tr>
                          </thead>
                          <tbody>
                            {turn.usage.calls.map((c, k) => (
                              <tr key={k} className="text-muted">
                                <td className="py-1 text-ink-dim">
                                  {c.step}
                                  <span className="ml-2 text-faint">{c.model}</span>
                                </td>
                                <td className="py-1 text-right">{c.prompt_tokens.toLocaleString("en-GB")}</td>
                                <td className="py-1 text-right">
                                  {c.completion_tokens > 0 ? c.completion_tokens.toLocaleString("en-GB") : "–"}
                                </td>
                                {turn.usage!.cost_usd != null && (
                                  <td className="py-1 text-right">{c.cost_usd != null ? money(c.cost_usd) : "–"}</td>
                                )}
                              </tr>
                            ))}
                            <tr className="text-ink-dim">
                              <td className="border-t border-border-soft pt-1.5">total</td>
                              <td className="border-t border-border-soft pt-1.5 text-right">
                                {turn.usage.prompt_tokens.toLocaleString("en-GB")}
                              </td>
                              <td className="border-t border-border-soft pt-1.5 text-right">
                                {turn.usage.completion_tokens.toLocaleString("en-GB")}
                              </td>
                              {turn.usage.cost_usd != null && (
                                <td className="border-t border-border-soft pt-1.5 text-right">{money(turn.usage.cost_usd)}</td>
                              )}
                            </tr>
                          </tbody>
                        </table>
                        <p className="mt-2 font-sans text-xs text-faint">
                          {turn.usage.cost_usd != null
                            ? "Computed from the registry's list price, USD per million tokens: the deployments at MiMo's own API whatever endpoint they were called at, embeddings at theirs. Not a bill: the provider's invoice is."
                            : "Tokens only: no price table is loaded. The server log says why."}
                        </p>
                      </div>
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
          className="max-w-[900px] bg-[linear-gradient(to_bottom,transparent,var(--color-bg)_30%)] px-4 pt-3 pb-4 sm:px-6 lg:px-10 [@media(max-height:500px)]:pt-1.5 [@media(max-height:500px)]:pb-2"
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
              placeholder={(welcome[asking] ?? welcome.en).placeholder}
              // 16px on a touch screen, and the same on the language select
              // below: iOS Safari zooms the page in when a focused field
              // renders under 16px and never zooms back out, leaving the app
              // permanently wider than the viewport. Never an inline
              // fontSize — it would out-specify the variant.
              className="block w-full resize-none bg-transparent px-1 py-2 text-[15px] text-ink outline-none pointer-coarse:text-base"
            />
            <div className="mt-1.5 flex flex-wrap items-center gap-2">
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
              {/* Pinned once the thread has a turn: dimmed, no chevron, no
                  hover — the pill stays where it was and still says which
                  language the thread is in, it just no longer offers a change.
                  The title says why, for the reader who tries. */}
              <label
                title={
                  threadLanguage
                    ? `This thread is answered in ${languages.find((l) => l.code === threadLanguage)?.name ?? threadLanguage}.`
                    : undefined
                }
                className={
                  "relative inline-flex h-9 items-center rounded-full border border-border bg-bg pl-3 text-xs text-muted sm:h-8 " +
                  (threadLanguage ? "pr-3 opacity-75" : "pr-2.5 hover:border-elevated-border hover:text-ink")
                }
              >
                <span className="sr-only">Answer language</span>
                <select
                  aria-label="Answer language"
                  value={asking}
                  disabled={threadLanguage !== null}
                  onChange={(e) => {
                    setLanguage(e.target.value);
                    rememberLanguage(e.target.value);
                  }}
                  className={
                    "lang-select border-0 bg-transparent text-inherit outline-none pointer-coarse:text-base " +
                    (threadLanguage ? "cursor-default opacity-100" : "cursor-pointer pr-4")
                  }
                >
                  {languages.map((l) => (
                    <option key={l.code} value={l.code}>
                      {l.name}
                    </option>
                  ))}
                </select>
                {!threadLanguage && (
                  <span className="pointer-events-none absolute right-2 rotate-90">
                    <Chevron />
                  </span>
                )}
              </label>
              {/* ml-auto belongs to the pair, not to the hint: the hint is not
                  rendered below sm, and with the push on it the Ask button
                  lost its right edge on exactly the width that needs it. */}
              <div className="ml-auto flex items-center gap-2">
                <span className="hidden text-xs text-faint sm:inline">Shift+Enter for a new line</span>
                <button
                  type="submit"
                  disabled={busy}
                  className="rounded-full bg-accent-fill px-4.5 py-1.5 text-sm font-medium text-ink hover:bg-accent-strong disabled:opacity-50 pointer-coarse:py-2.5"
                >
                  Ask
                </button>
              </div>
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
