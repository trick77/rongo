import { useEffect, useMemo, useRef, useState } from "react";
import ThreadView, { SourcesPane } from "./ThreadView";
import SourceView from "./SourceView";
import { Chevron } from "./icons";
import {
  asMarkdown,
  freshTurn,
  headOf,
  languages,
  linkChosenCandidates,
  roleName,
  storedRetries,
  storedTurn,
  threadUsage,
  type Audience,
  type Citation,
  type Message,
  type Turn,
  type Usage,
} from "./turns";

// The pieces of a turn the rest of the app still reaches for through Ask.
// Re-exported rather than moved twice: App imports money and threadUsage for
// the header's running total, and the tests import languages.
export { languages, money, threadUsage } from "./turns";
export type { Usage, UsageCall } from "./turns";

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
  /** Whether a turn is running, and the thread it is being written into. */
  onBusy?: (busy: boolean, threadId: number | null) => void;
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
  // The marker under the pointer, reported up by ThreadView so the pane in
  // the column beside it can point back. Everything else the reading half
  // remembers — the open usage block, the unfolded failure, which turn has
  // just been copied — lives inside ThreadView, where it belongs.
  const [hot, setHot] = useState<number | null>(null);
  // The source open in the viewer, or null while it is closed. Page-level:
  // the overlay covers the whole app, and both the text and the pane beside
  // it open one.
  const [viewing, setViewing] = useState<Citation | null>(null);
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
  // arrives into it. It follows from the moment a turn is asked and lets go
  // for the rest of that turn as soon as the reader touches the column —
  // scrolling it, taking hold of the text, opening a source. Coming back to
  // the foot does not hand the view back: the answer is theirs to read at
  // their own pace until the next question is asked.
  const view = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  // Where this view put itself last. A reader's scroll is told from the
  // view's own by the position it lands at, not by the distance to the foot:
  // the answer grows between the scroll and its event, so a measured distance
  // reads as "they left" on a batch of tokens nobody touched.
  const selfTop = useRef(0);
  function stopFollowing() {
    following.current = false;
  }
  // Every scroll this view makes itself goes through here, so the event it
  // provokes is recognised when it arrives.
  function scrollSelf(el: HTMLDivElement, top: number) {
    el.scrollTop = top;
    selfTop.current = el.scrollTop;
  }
  // Where a touch began, so a drag can be read as a direction. Only a drag
  // back UP the thread is the reader leaving: a finger travelling up the
  // glass takes the column towards the foot, which is where the answer is
  // being written anyway — and at the foot iOS answers a downward pull with
  // a rubber-band bounce that moves nothing at all.
  const touchY = useRef(0);
  function noteTouch(e: React.TouchEvent) {
    touchY.current = e.touches[0]?.clientY ?? 0;
  }
  function touchLeaves(e: React.TouchEvent) {
    // A few pixels of slack: a finger resting on the glass jitters, and a tap
    // that happens to wobble is not a scroll.
    if ((e.touches[0]?.clientY ?? 0) - touchY.current > 4) stopFollowing();
  }
  // A wheel only counts upwards. Asking for more of what is arriving — a
  // nudge down at the foot, the commonest gesture of all — moves nothing and
  // must not cost the reader the follow, and a horizontal wheel belongs to a
  // code block inside the answer, not to the column.
  function wheelLeaves(e: React.WheelEvent) {
    if (e.deltaY < 0) stopFollowing();
  }
  // A source opens from the pane beside the thread as well as from the text,
  // and the pane is outside the scrolling column, so its click reaches none of
  // the column's own handlers.
  function showSource(c: Citation) {
    stopFollowing();
    setViewing(c);
  }
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
  // The per-turn marker handlers that used to sit here are ThreadView's now,
  // with the rest of the reading half. `live` stays: appendTurn and patchLast
  // read it to write into the turn being streamed.

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

  /**
   * The thread a turn is being written into, and its whole conversation as the
   * stream has it so far. The pair is what lets the reader walk away from an
   * answer and come back to it: the tokens keep landing in this list whether or
   * not it is the one on screen, and opening that thread again restores it
   * instead of fetching a record the turn has not reached yet.
   *
   * A ref, not state: every token patches it, and the arrival has to be there
   * for the next patch in the same tick rather than after a commit.
   */
  const liveThread = useRef<number | null>(null);
  const liveTurns = useRef<Turn[] | null>(null);

  /**
   * Appends a turn and hands the new list to the stream that is about to write
   * into it. Every path that asks something goes through here — a question, a
   * resumed card, a re-explain, a retry, a suggestion — because every one of
   * them is a turn the reader may walk away from.
   */
  function appendTurn(t: Turn, edit: (list: Turn[]) => Turn[] = (l) => l) {
    const next = [...edit(live.current), t];
    // live mirrors the rendered turns and is what the next patch reads, so it
    // is moved on now rather than at the commit this schedules.
    live.current = next;
    liveTurns.current = next;
    liveThread.current = threadId.current;
    setTurns(next);
  }

  function patchLast(patch: (t: Turn) => Turn) {
    const parked = liveTurns.current;
    // Only a running turn is ever patched, and a running turn always has its
    // parked list: every path into stream() appends through appendTurn first,
    // and the list is dropped only once the stream is over.
    if (!parked) return;
    const next = parked.map((t, i) => (i === parked.length - 1 ? patch(t) : t));
    liveTurns.current = next;
    // Only the thread on screen is repainted. The reader may be reading
    // another conversation entirely, and writing this turn into it is exactly
    // what the old lock existed to prevent.
    if (shown.current === liveThread.current) {
      live.current = next;
      setTurns(next);
    }
  }

  // Announced upwards with the thread it belongs to: the rail withholds that
  // one row's actions, and the composer says an answer is still arriving even
  // when the reader has moved to a thread where nothing is happening.
  function markBusy(b: boolean, id: number | null = null) {
    setBusy(b);
    onBusy(b, id);
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
    // The total in the header belongs to the old thread until the new one has
    // loaded. The per-turn state that is also an index into the thread being
    // left — the open breakdown, the unfolded failure — is ThreadView's, and
    // it drops with the turns it belonged to.
    onUsage(null);
    // The thread being written comes back from the parked copy, never from the
    // server: the record has no answer on it yet — the row is only finished
    // when the turn is — so a fetch would replace a half-written answer with
    // an empty one and the rest of the tokens would land out of sight.
    if (openThread !== null && openThread === liveThread.current && liveTurns.current) {
      const parked = liveTurns.current;
      live.current = parked;
      setTurns(parked);
      setLoading(false);
      onUsage(threadUsage(parked));
      // Not an opened record but an answer in flight: the view belongs at the
      // foot, following what is still arriving, exactly as it did when the
      // reader left it.
      opened.current = false;
      following.current = true;
      return;
    }
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
        const list = (await res.json()) as Message[];
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
    if (view.current) selfTop.current = view.current.scrollTop;
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
      scrollSelf(el, 0);
      return;
    }
    if (following.current) scrollSelf(el, el.scrollHeight);
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
    markBusy(true, liveThread.current);
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
            // The turn was appended before the thread existed, so this is
            // where it learns which conversation it is parked under. Always,
            // whatever the reader is looking at: the parking is what keeps
            // the tokens out of the thread in front of them.
            liveThread.current = payload.thread_id;
            markBusy(true, payload.thread_id);
            // The rest only if the reader is still standing where they asked.
            // A first question whose thread id arrives after they have opened
            // another conversation must not drag them back to it — and
            // threadId, which is where the NEXT question is filed, belongs to
            // the thread they are in, not to the one they left.
            if (shown.current === null) {
              threadId.current = payload.thread_id;
              shown.current = payload.thread_id;
              onThread(payload.thread_id);
            }
            // The turn is on record now, in the language the record took. That
            // is not always the one that was asked for — a thread answers in
            // the language of its first turn — so the turn on screen, and with
            // it the composer, follow the server rather than the guess.
            patchLast((t) => ({ ...t, recorded: true, language: payload.language ?? t.language }));
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
            // The id comes with the failure too, not only with done: asking
            // again is another attempt at THIS question, and the request can
            // only say so if the turn knows which row it is.
            patchLast((t) => ({
              ...t,
              error: payload.message,
              done: true,
              endedAt: Date.now(),
              messageId: payload.message_id ?? t.messageId,
            }));
          }
          else if (name === "done") {
            closed = true;
            patchLast((t) => ({
              ...t,
              done: true,
              endedAt: t.endedAt ?? Date.now(),
              messageId: payload.message_id ?? t.messageId,
              // A re-explain opens no thread event, and it too is filed in the
              // thread's language rather than the one it asked for.
              recorded: true,
              language: payload.language ?? t.language,
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
      // The turn is over, so the parked copy is dropped: from here the record
      // is complete — answer, citations, suggestions and all — and coming back
      // to the thread reads it from the server like any other.
      liveTurns.current = null;
      liveThread.current = null;
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
   * Null until a turn is on record — a first question that failed before the
   * server wrote its row left a turn on screen but nothing in the thread, and
   * locking the composer to it would strand the reader in a language they
   * cannot change.
   */
  const threadLanguage = turns.find((t) => t.recorded)?.language ?? null;
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

    appendTurn(freshTurn(q, audience, asking));
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
    appendTurn(
      freshTurn(turn.question, turn.audience, turn.language, headOf(turn)),
      (list) => list.map((t, i) => (i === turnIndex ? { ...t, chosenIdx: idx } : t)),
    );

    // The card belongs to the thread it was asked in, and turnIndex is an
    // index into that thread's turns — in another one it points at a
    // different turn entirely.
    const cardThread = threadId.current;
    const ok = await stream("/api/ask", {
      thread_id: threadId.current ?? 0,
      question: turn.question,
      audience: turn.audience,
      language: turn.language,
      clarification_message_id: turn.clarification.messageId,
      choice: idx,
    }, false);
    // A reader who has moved on gets the unlock from the record instead: the
    // choice is stored only when an answer lands, so the card they come back
    // to is open again anyway.
    if (!ok && shown.current === cardThread) {
      setTurns((prev) => prev.map((t, i) => (i === turnIndex ? { ...t, chosenIdx: null } : t)));
    }
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
    appendTurn(freshTurn(turn.question, nextAudience, turn.language, headOf(turn)));

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
    appendTurn(freshTurn(question, turn.audience, turn.language));

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

    const head = headOf(turn);

    retireLoad();
    appendTurn(freshTurn(turn.question, turn.audience, turn.language, head));

    // The thread id is taken now, not from the stored body: the thread may
    // have been created by the very turn that failed, and a stored turn's
    // request carries none at all.
    //
    // The head is taken now too. A turn that failed on a freshly typed
    // question was sent without one — there was nothing to join yet — and it
    // only learned its own id when the failure came back. Asking again is
    // another attempt at that same question, so it says so.
    await stream(
      req.url,
      req.url === "/api/ask"
        ? { ...req.body, thread_id: threadId.current ?? 0, head_message_id: head }
        : req.body,
    );
  }

  // The six things a reader can do to a turn. ThreadView draws the turns and
  // knows nothing about the stream; this is the whole of what it can set off.
  // A memo, because a fresh object per render would remount every turn on
  // every streamed token.
  //
  // Both copies report whether the clipboard took it: in an insecure context,
  // or with the permission refused, a button saying "Copied" over a clipboard
  // that still holds whatever was there before is a plain lie. Nothing else is
  // said about it — the text is on screen to select, and a banner would be
  // noise.
  const actions = useMemo(
    () => ({
      onRetry: retry,
      onReexplain: reexplain,
      onCopy: async (i: number) => {
        try {
          await navigator.clipboard.writeText(asMarkdown(live.current[i]));
          return true;
        } catch {
          return false;
        }
      },
      // The question alone, as it was typed. Not asMarkdown's heading: this
      // is for quoting the question somewhere else — a ticket, a message, the
      // composer of another thread — and a `# ` in front of it is rongo's
      // formatting, not the reader's words. The full text is always in the
      // DOM, so a folded question copies whole.
      onCopyQuestion: async (i: number) => {
        try {
          await navigator.clipboard.writeText(live.current[i].question);
          return true;
        } catch {
          return false;
        }
      },
      onFollowup: askFollowup,
      onChoose: chooseCandidate,
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [turns],
  );

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
          // The reader's own intent, caught as it happens: a scroll event is
          // delivered a beat later, and by then the next token has already
          // pulled the view back to the foot, so the reader watches their
          // scroll being undone. Wheel and touch are the two ways of moving
          // the column, and only away from the foot counts; a pointer going
          // down in it is a text selection, a scrollbar drag, or a chip or
          // diagram being opened — all of them reasons to stop writing under
          // the reader's hands.
          onWheel={wheelLeaves}
          onTouchStart={noteTouch}
          onTouchMove={touchLeaves}
          onPointerDown={stopFollowing}
          // The net under all of it: find-in-page, a scroll restored by the
          // browser, anything that moves the column without an intent event.
          // It only ever lets go — the view's own scrolls land at the
          // position recorded for them, and nothing here takes the view back.
          // A scroll that comes to rest at the foot is never the reader
          // leaving it: markdown resolving mid-stream (a fence closing, a row
          // becoming a table) shortens the column, and the browser's own
          // clamp arrives as a scroll nobody asked for. Latched, that would
          // stop the follow for good on a turn the reader never touched.
          onScroll={() => {
            const el = view.current;
            if (!el || el.scrollTop === selfTop.current) return;
            // A few pixels of foot rather than none: scrollHeight and
            // clientHeight are whole numbers while scrollTop is snapped to
            // device pixels, so at a fractional device ratio the true foot
            // reads a pixel or two short of it.
            if (el.scrollHeight - el.scrollTop - el.clientHeight > 4) stopFollowing();
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

            <ThreadView
              turns={turns}
              busy={busy}
              actions={actions}
              onOpenSource={showSource}
              onHot={setHot}
              threadKey={openThread}
            />
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
              {/* Only while the choice is still open. A thread answers in the
                  language of its first question, so from the second turn on
                  this offered nothing: it stood there pinned and dimmed, a
                  control that refused every hand laid on it. The turn's own
                  pill above the answer already says which language the thread
                  is in, and it says it where the answer is. */}
              {!threadLanguage && (
                <label className="relative inline-flex h-9 items-center rounded-full border border-border bg-bg pr-2.5 pl-3 text-xs text-muted hover:border-elevated-border hover:text-ink sm:h-8">
                  <span className="sr-only">Answer language</span>
                  <select
                    aria-label="Answer language"
                    value={asking}
                    onChange={(e) => {
                      setLanguage(e.target.value);
                      rememberLanguage(e.target.value);
                    }}
                    className="lang-select cursor-pointer border-0 bg-transparent pr-4 text-inherit outline-none pointer-coarse:text-base"
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
              )}
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
            {/* The Ask button is dead here and the reason is somewhere else
                entirely — a turn still being written in another thread. A
                dimmed button with no explanation is the thing this whole
                change is about. Only when the answer is out of sight: in the
                thread being written, the running turn is right above. */}
            {busy && liveThread.current !== shown.current && (
              <p className="mt-2 px-1 text-xs text-muted">
                Another thread is still being answered — the next question waits for it.
              </p>
            )}
          </div>
        </form>
      </div>

      <SourcesPane turns={turns} hot={hot} onOpen={showSource} />

      {viewing && <SourceView source={viewing} onClose={() => setViewing(null)} />}
    </div>
  );
}
