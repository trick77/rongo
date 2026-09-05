import { useEffect, useMemo, useRef, useState } from "react";
import Markdown from "./markdown";
import Clarify from "./Clarify";
import Question from "./Question";
import Trace from "./Trace";
import { CheckIcon, Chevron, CopyIcon } from "./icons";
import {
  clock,
  forgeLine,
  groupByQuestion,
  languages,
  money,
  pill,
  roleName,
  stageLabel,
  tokens,
  traceState,
  type Citation,
  type Turn,
} from "./turns";

/**
 * The reading half of a thread: the turns, and the sources they were written
 * from. It was Ask's own render until the share page needed the same thing
 * without a composer, a stream or a way to change anything.
 *
 * `actions` is the whole difference between the two callers. Ask passes every
 * one of them; the share page passes null, and the footer, the follow-up
 * chips, Retry and the candidate buttons are simply not rendered — a reader
 * with no session has no move to make, and offering one would be a lie.
 *
 * What the view remembers about itself — which failure is unfolded, which
 * usage block is open, which turn has just been copied — lives here rather
 * than in the caller. None of it survives leaving the thread, and none of it
 * is any of Ask's business. The source viewer is the exception, and is the
 * caller's: it is a page-level overlay, and the pane beside the thread opens
 * it from a different cell of the caller's grid.
 */
export type ThreadActions = {
  onRetry: (i: number) => void;
  onReexplain: (i: number) => void;
  /** Reports whether the clipboard took it: the label must not say so if not. */
  onCopy: (i: number) => Promise<boolean>;
  onCopyQuestion: (i: number) => Promise<boolean>;
  onFollowup: (i: number, question: string) => void;
  onChoose: (i: number, idx: number) => void;
};

export type ThreadViewProps = {
  turns: Turn[];
  /** Locks every action while a turn is in flight. Always false read-only. */
  busy?: boolean;
  actions?: ThreadActions | null;
  /**
   * Opens a cited file. The viewer is the caller's, not this component's: it
   * is a page-level overlay, and the pane beside the thread opens it too from
   * a different cell of the caller's grid.
   */
  onOpenSource: (c: Citation) => void;
  /** The pane beside the thread renders in the caller's own grid cell, so it
   * reads the hot marker through this. */
  onHot?: (marker: number | null) => void;
  /**
   * Which thread these turns are. Everything this view remembers is an INDEX
   * into them, and the same index in the next thread is a different turn — so
   * a change here drops the lot rather than opening a breakdown nobody
   * clicked. Null is the unasked new question.
   */
  threadKey?: number | string | null;
};

/** The newest turn that cited anything: the one the pane shows, and the only
 * one whose markers can be pointed back to. */
export function sourceTurnOf(turns: Turn[]): number {
  for (let i = turns.length - 1; i >= 0; i--) if (turns[i].citations.length > 0) return i;
  return -1;
}

export default function ThreadView({
  turns,
  busy = false,
  actions = null,
  onOpenSource,
  onHot,
  threadKey = null,
}: ThreadViewProps) {
  const [copied, setCopied] = useState<number | null>(null);
  // The question copied, by the index of the turn it was asked in. Its own
  // state: copying the question and copying the answer are two controls, and
  // one flag would light both.
  const [copiedQuestion, setCopiedQuestion] = useState<number | null>(null);
  // The turn whose usage breakdown is open, if any. One at a time: it is a
  // glance at what a turn cost, not a report to keep open.
  const [openUsage, setOpenUsage] = useState<number | null>(null);
  // The superseded failures the reader has unfolded. A failure stays in the
  // record and stays on the page, but a turn that went on to answer should
  // not open with the attempt that broke — so it folds to a line, and the
  // line says what it is.
  const [openFailure, setOpenFailure] = useState<Set<number>>(new Set());
  function toggleFailure(i: number) {
    setOpenFailure((prev) => {
      const next = new Set(prev);
      if (!next.delete(i)) next.add(i);
      return next;
    });
  }

  // Another thread: the indices this view is holding mean something else now.
  useEffect(() => {
    setOpenUsage(null);
    setOpenFailure(new Set());
    setCopied(null);
  }, [threadKey]);

  const setHot = (marker: number | null) => onHot?.(marker);
  const showSource = onOpenSource;

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
        if (c) showSource(c);
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

  async function copy(turnIndex: number) {
    if (!actions) return;
    // Only on a clipboard that actually took it. In an insecure context, or
    // with the permission refused, the button saying "Copied" would be a
    // plain lie — and the reader would paste whatever was there before.
    if (!(await actions.onCopy(turnIndex))) return;
    setCopied(turnIndex);
    setTimeout(() => setCopied(null), 1500);
  }

  async function copyQuestion(turnIndex: number) {
    if (!actions) return;
    if (!(await actions.onCopyQuestion(turnIndex))) return;
    setCopiedQuestion(turnIndex);
    setTimeout(() => setCopiedQuestion(null), 1500);
  }

  // One article per question. The list itself stays flat — every action here
  // addresses a turn by its position in it — and only the rendering groups.
  const groups = useMemo(() => groupByQuestion(turns), [turns]);

  const sourceTurnIndex = sourceTurnOf(turns);

  // A highlight belongs to the turn the pane shows. When the pane moves to a
  // newer turn, the old Markdown's mouseleave never fires for it.
  useEffect(() => {
    onHot?.(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourceTurnIndex]);

  // Read by the moved block below, which still names the two exactly as it
  // did inside Ask.
  const reexplain = (i: number) => actions?.onReexplain(i);
  const retry = (i: number) => actions?.onRetry(i);
  const askFollowup = (i: number, q: string) => actions?.onFollowup(i, q);
  const chooseCandidate = (i: number, idx: number) => actions?.onChoose(i, idx);

  return (
    <>
            {groups.map((group, g) => {
              const asked = turns[group[0]];
              return (
              <article
                key={group[0]}
                className="mb-8 border-b border-border-soft pb-8 last:mb-0 last:border-b-0 [@media(max-height:500px)]:mb-4 [@media(max-height:500px)]:pb-4"
              >
                <div className="text-[11px] font-medium uppercase tracking-[.1em] text-accent-strong">
                  {roleName(asked.audience)}
                </div>
                {/* The accent is the eyebrow's alone now: the question is what
                    was typed, at whatever length it was typed, and it reads as
                    the reader's words rather than as a headline.

                    Once, because it was asked once. Everything below is what
                    came of it — a card, a failure, the answer, the same answer
                    for the other audience — and each of those is a row in the
                    record carrying a copy of these words. Printing the copies
                    would say the reader typed the question again, which they
                    did not. */}
                <Question text={asked.question} />
                <div className="mt-2.5 flex items-center gap-1.5">
                  {asked.askedAt && <time className="font-mono text-[11.5px] text-faint">{clock(asked.askedAt)}</time>}
                  {/* Counted in questions, not in rows: a turn asked twice
                      because the first attempt broke is still the first turn. */}
                  <span className={pill + " bg-active text-muted"}>Turn {g + 1}</span>
                  {asked.language !== "en" && (
                    <span className={pill + " bg-active text-muted"}>
                      {languages.find((l) => l.code === asked.language)?.name ?? asked.language}
                    </span>
                  )}
                  {/* The question's own copy, next to the words it copies.
                      The answer's footer copies the whole turn as Markdown,
                      which is the wrong thing to paste into a ticket or the
                      composer of another thread; and selecting the prose by
                      hand fights a question folded at three lines.

                      Always drawn, never revealed on hover: a phone has no
                      hover, the same reason the diagram toolbar gives. Not on
                      a shared page, where the footer's copy is gone too — one
                      copy control without the other would read as an
                      oversight rather than as a decision. */}
                  {actions && (
                    <button
                      type="button"
                      onClick={() => copyQuestion(group[0])}
                      aria-label={copiedQuestion === group[0] ? "Question copied" : "Copy the question"}
                      title={copiedQuestion === group[0] ? "Question copied" : "Copy the question"}
                      className="-my-1 ml-0.5 grid h-8 w-8 place-items-center rounded-ui-sm text-faint transition-colors hover:bg-active hover:text-ink-dim sm:h-7 sm:w-7"
                    >
                      {copiedQuestion === group[0] ? <CheckIcon /> : <CopyIcon />}
                    </button>
                  )}
                </div>

                {/* One entry per attempt. A turn answered on the first try has
                    exactly one and looks as it always did: no rail, no label,
                    nothing added for a thread that never needed grouping. */}
                <div className={group.length > 1 ? "mt-3.5 grid gap-5 border-l-2 border-border pl-4" : ""}>
                {group.map((i, k) => {
                  const turn = turns[i];
                  // A failure the reader has already moved past folds to a
                  // line. The last one in a turn never folds: its Retry button
                  // is the only way on from there.
                  const superseded = !!turn.error && k < group.length - 1;
                  // The turn's own answer: the first attempt that neither
                  // asked back nor broke. Everything answered after it is a
                  // re-explain of it.
                  const firstAnswer =
                    group.find((j) => !turns[j].clarification && !turns[j].error) === i;
                  const folded = superseded && !openFailure.has(i);
                  return (
                    <div key={i}>
                {group.length > 1 && (
                  <div className="flex items-center gap-2 text-[10.5px] font-semibold uppercase tracking-[.09em] text-faint">
                    <span className="-ml-[21px] h-1.5 w-1.5 rounded-full bg-muted outline-3 outline-bg" />
                    {stageLabel(turn, firstAnswer)}
                    {turn.askedAt && <time className="font-mono text-[11px] font-normal normal-case tracking-normal">{clock(turn.askedAt)}</time>}
                    {superseded && (
                      <button
                        type="button"
                        onClick={() => toggleFailure(i)}
                        className="font-sans text-[11px] font-normal normal-case tracking-normal text-muted underline decoration-border underline-offset-2 hover:text-ink"
                      >
                        {folded ? "Show" : "Hide"}
                      </button>
                    )}
                  </div>
                )}
                {!folded && (
                  <>

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
                    readOnly={!actions}
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
                    {turn.retry && actions && (
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
                            onClick={() => showSource(c)}
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
                {actions && i === turns.length - 1 && turn.done && turn.followups.length > 0 && (
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
                {actions && turn.done && (turn.usage || (turn.messageId && !turn.error && !turn.clarification)) && (
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
                  </>
                )}
                    </div>
                  );
                })}
                </div>
              </article>
              );
            })}
    </>
  );
}

/**
 * The files an answer was written from, beside the thread. A cell of the
 * caller's own grid rather than part of ThreadView: the column scrolls and
 * this does not, so the two cannot live in one element.
 */
export function SourcesPane({
  turns,
  hot,
  onOpen,
}: {
  turns: Turn[];
  hot: number | null;
  onOpen: (c: Citation) => void;
}) {
  const sourceTurnIndex = sourceTurnOf(turns);
  const sourceTurn = sourceTurnIndex >= 0 ? turns[sourceTurnIndex] : null;
  const groups = useMemo(() => groupByQuestion(turns), [turns]);
  return (
    <aside aria-label="Sources" className="hidden min-h-0 flex-col border-l border-border bg-panel xl:flex">
      <header className="flex items-center border-b border-border px-4.5 py-3.5 text-[11px] font-medium uppercase tracking-[.12em] text-faint">
        Sources
        {sourceTurn && (
          <span className="ml-auto font-mono tracking-normal">
            {/* The turn the reader sees, counted in questions like the pill
                on the article — not the row's place in the record. */}
            turn {groups.findIndex((g) => g.includes(sourceTurnIndex)) + 1} · {sourceTurn.citations.length}
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
            onClick={() => onOpen(c)}
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
  );
}
