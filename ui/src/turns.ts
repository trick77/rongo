/**
 * A turn, and everything that shapes one into what the page shows.
 *
 * Split out of Ask so the reading half of a thread has a home of its own:
 * ThreadView draws these, Ask streams into them, and the share page — which
 * has no composer and no stream at all — reads them straight off the record.
 * Nothing here talks to the network; that stays in the views.
 *
 * Every export was already in Ask.tsx and is unchanged; the only edits are the
 * `export` keywords the second reader needs.
 */
import { type ClarifyCandidate } from "./Clarify";
import { type Step, type TraceState } from "./Trace";
import { type SourceRef } from "./SourceView";
import { mermaidize } from "./diagramExport";

export type Citation = SourceRef;

export type Audience = "ba" | "dev";

/** The languages the answer can be written in; the backend's allowlist. */
export const languages: { code: string; name: string }[] = [
  { code: "en", name: "English" },
  { code: "de", name: "Deutsch" },
  { code: "fr", name: "Français" },
  { code: "it", name: "Italiano" },
];
/** The clarification a turn ended with, as this view needs it: the id of the
 * message that carries the card (used to resume it) and its candidates. */
export type TurnClarification = {
  messageId: number;
  candidates: ClarifyCandidate[];
};

/** What a failed turn is asked again with: the endpoint and the body of the
 * request that failed, kept so a retry re-issues exactly that. Null on a turn
 * that has nothing to offer — one still running, one that answered, and a
 * resume, whose card is its own retry. */
export type RetryRequest = {
  url: string;
  body: Record<string, unknown>;
};

export type Turn = {
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
  // Whether the server has a row for this turn. A turn that failed before the
  // record was written is on screen but not in the thread, and must not pin
  // the thread's language — the reader has to be able to pick another one and
  // try again.
  recorded: boolean;
  // The question this turn is an attempt at: the message id of the turn that
  // carried it, or null when this turn IS that one. A resume, a retry and a
  // re-explain all repeat the question text of the turn they continue — the
  // thread is a record and nothing in it is rewritten — so this link is the
  // only thing that says the question was asked once, and it is what the view
  // groups by. Without it the page would print the same question three times
  // and claim it was typed three times.
  headId: number | null;
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
export function tokens(n: number): string {
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
export type Message = {
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
  // The turn this row is an attempt at, or 0 when the row IS the turn. Absent
  // on a row written before the column existed, which stays its own turn.
  head_message_id?: number;
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
export function storedTurn(m: Message): Turn {
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
    recorded: true,
    headId: m.head_message_id || null,
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
 *
 * headId is the question this one is another attempt at. Null only when the
 * reader typed it: a resume, a retry and a re-explain all name the turn they
 * belong to, because the words are the same and nothing else would tell the
 * record they are one question.
 */
export function freshTurn(question: string, audience: Audience, language: string, headId: number | null = null): Turn {
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
    recorded: false,
    done: false,
    messageId: null,
    headId,
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
export function linkChosenCandidates(list: Message[], turns: Turn[]): Turn[] {
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

/** headOf is the turn a stored turn belongs to: the question it is an attempt
 * at, or its own id when it IS that question. Null only while a turn asked in
 * this session has no id yet. */
export function headOf(t: Turn): number | null {
  return t.headId ?? t.messageId;
}

/**
 * Gives every failed turn in a restored thread the request that asks it
 * again, and points that request at the turn it retries so the record keeps
 * saying the question was asked once.
 *
 * The card exception keeps the resume rule holding across a reload: a failed
 * turn belonging to a card nobody has answered IS that card's resume. Its
 * retry is the card, which is still open, so it gets no request and no button
 * — two ways of spending the same resume on screen would be one too many.
 *
 * For a turn that carries the link the group is asked, not inferred. A turn
 * written before the column existed carries none, and never will: a resume is
 * linked to its card only when an answer lands, so a resume that FAILED back
 * then left nothing behind at all and no backfill can reach it. Those keep the
 * old rule — walk back for an open card on the same question, skipping the
 * failures in between, because a card can be tried more than once. It is the
 * guess it always was, and it stays only where there is nothing to read.
 */
export function storedRetries(turns: Turn[]): Turn[] {
  return turns.map((t, i) => {
    if (!t.error) return t;
    const head = headOf(t);
    const openCard = t.headId
      ? turns.some((o) => headOf(o) === head && o.clarification && o.chosenIdx == null)
      : (() => {
          let j = i - 1;
          while (j >= 0 && turns[j].error && !turns[j].clarification && turns[j].question === t.question) j--;
          const card = j >= 0 ? turns[j] : null;
          return !!card && !!card.clarification && card.chosenIdx == null && card.question === t.question;
        })();
    if (openCard) return t;
    return {
      ...t,
      retry: {
        url: "/api/ask",
        body: {
          question: t.question,
          audience: t.audience,
          language: t.language,
          // The turn this asks again, so the retry lands in it rather than
          // filing the same question a second time.
          head_message_id: head,
        },
      },
    };
  });
}

/** The trace's states — the turn does not merely finish or not: it can end
 * by asking (and that closes on the waiting node, not the check), lose the
 * colour once the reader has chosen, or break. */
export function traceState(turn: Turn): TraceState {
  if (turn.error) return "failed";
  if (turn.clarification) return turn.chosenIdx == null ? "waiting" : "decided";
  return turn.done ? "done" : "running";
}

/** How a citation reads on one line: in the list under an answer, and in the
 * Markdown a turn is copied as. */
export function forgeLine(c: Citation): string {
  return `${c.repo} · ${c.path}:${c.start_line}-${c.end_line} (${c.branch})`;
}

export function roleName(a: Audience): string {
  return a === "dev" ? "Developer" : "Analyst";
}

/**
 * What one attempt inside a turn is called. Only ever shown for a turn that
 * took more than one: the label answers "why is there a second block under
 * the same question", and there is nothing to answer when there is only one.
 *
 * The audience rides on the attempt rather than on the turn, because a
 * re-explain is the same question answered for the other reader — not a new
 * question, and the eyebrow above already said who asked.
 *
 * first is whether this is the turn's own answer, the one every re-explain is
 * built from. Read off position rather than off the audience: explaining a
 * Developer answer for the Analyst and then back again lands on the audience
 * the turn started with, and that third block is still a re-explain.
 */
export function stageLabel(turn: Turn, first: boolean): string {
  if (turn.clarification) return "Clarification";
  if (turn.error) return "Failed";
  if (!first) return `Re-explained · ${roleName(turn.audience)}`;
  return `Answer · ${roleName(turn.audience)}`;
}

/**
 * Groups the turns by the question they are attempts at, keeping the order
 * they were recorded in. One group is one article: the question printed once,
 * with everything that came of it under it.
 *
 * A turn asked in this session has no id until the server answers, so it
 * keys on its position until then. Nothing can join it before it has one —
 * the actions that would are all disabled while a turn is in flight.
 */
export function groupByQuestion(turns: Turn[]): number[][] {
  const at = new Map<number | string, number>();
  const out: number[][] = [];
  turns.forEach((t, i) => {
    const key = headOf(t) ?? `live-${i}`;
    const g = at.get(key);
    if (g === undefined) {
      at.set(key, out.length);
      out.push([i]);
    } else {
      out[g].push(i);
    }
  });
  return out;
}

export function clock(iso: string): string {
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
export function asMarkdown(turn: Turn): string {
  const lines = [`# ${turn.question}`, "", mermaidize(turn.text.trim())];
  if (turn.citations.length > 0) {
    lines.push("", "Sources:", ...turn.citations.map((c) => `[${c.marker}] ${forgeLine(c)}`));
  }
  return lines.join("\n") + "\n";
}

export const pill = "rounded-full px-2.5 py-0.5 text-xs whitespace-nowrap";
