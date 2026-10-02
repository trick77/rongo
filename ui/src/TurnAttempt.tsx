import { memo, type ReactNode } from "react";
import Markdown from "./markdown";
import Clarify from "./Clarify";
import Narrow from "./Narrow";
import Trace from "./Trace";
import MemoryChip from "./memory/MemoryChip";
import { clock, money, pill, tokens, traceState, type Turn } from "./turns";
import type { ThreadActions } from "./ThreadView";

/**
 * One attempt at a turn: its trace, its card or its answer, and the actions
 * on it. Cut out of ThreadView so that it can be memoized.
 *
 * A streamed token replaces the list of turns and the one turn being written;
 * every other turn is the object it was. Drawn inline, all of them were drawn
 * again for every token anyway — the trace, the card, the chips, the footer of
 * each — and a long thread was redrawn sixty times a second to append a word
 * to its last answer. Memoized, a turn is drawn again when something of its
 * own changed.
 *
 * Which is why every prop here is either a value of this turn or a function
 * that stays the same between renders: ThreadView holds the handlers steady
 * and passes the index for them to be called with. A fresh arrow in the
 * parent would undo the whole thing, silently —
 * ThreadViewRenders.test.tsx is what would say so.
 */
export type TurnAttemptProps = {
  turn: Turn;
  /** The turn's position in the thread: what every action addresses it by. */
  i: number;
  /** What this attempt was, when its turn took more than one; else null. */
  label: string | null;
  /** A failure the reader has moved past, and whether it is folded away. */
  superseded: boolean;
  folded: boolean;
  busy: boolean;
  /** Null on a shared page: nothing to do, so nothing is offered. */
  actions: ThreadActions | null;
  /** The newest turn of the thread: the only one that offers what to ask next. */
  isLast: boolean;
  /** Set on the turn the sources pane lists, whose markers light its rows. */
  onMarkerHover?: (marker: number | null) => void;
  onMarkerOpen: (marker: number) => void;
  backed?: Set<number>;
  /** The pane is open on this very turn. */
  sourcesExpanded: boolean;
  onToggleSources?: (i: number) => void;
  /** This turn's answer has just been copied. */
  copied: boolean;
  onCopy: (i: number) => void;
  onToggleFailure: (i: number) => void;
  /** The thread's verdict, among this answer's buttons. ThreadView hands it
   * to the newest answer only, and holds the element steady across renders. */
  feedback?: ReactNode;
};

function TurnAttempt({
  turn,
  i,
  label,
  superseded,
  folded,
  busy,
  actions,
  isLast,
  onMarkerHover,
  onMarkerOpen,
  backed,
  sourcesExpanded,
  onToggleSources,
  copied,
  onCopy,
  onToggleFailure,
  feedback = null,
}: TurnAttemptProps) {
  return (
    <div>
                {label !== null && (
                  <div className="flex items-center gap-2 text-[10.5px] font-semibold uppercase tracking-[.09em] text-faint">
                    <span className="-ml-[21px] h-1.5 w-1.5 rounded-full bg-muted outline-3 outline-bg" />
                    {label}
                    {turn.askedAt && <time className="font-mono text-[11px] font-normal normal-case tracking-normal">{clock(turn.askedAt)}</time>}
                    {superseded && (
                      <button
                        type="button"
                        onClick={() => onToggleFailure(i)}
                        className="font-sans text-[11px] font-normal normal-case tracking-normal text-muted underline decoration-border underline-offset-2 hover:text-ink"
                      >
                        {folded ? "Show" : "Hide"}
                      </button>
                    )}
                  </div>
                )}
                {!folded && (
                  <>

                {/* Not on `live` alone: a turn read back out of the record has
                    a timeline too, and it is the same timeline. A live turn
                    still draws one before its first step arrives — that empty
                    frame is the turn starting — while a stored turn with no
                    steps, which is every turn older than the column, draws
                    nothing. `live` keeps its own meaning below, where it
                    decides whether the answer fades in as it arrives. */}
                {(turn.live || turn.steps.length > 0) && (
                  <Trace steps={turn.steps} state={traceState(turn)} startedAt={turn.startedAt} endedAt={turn.endedAt} live={turn.live} />
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

                {turn.clarification &&
                  (turn.clarification.tooBroad ? (
                    <Narrow
                      repos={turn.clarification.candidates.map((c) => ({
                        repo: c.repo,
                        branch: c.branch,
                        members: c.members,
                      }))}
                      narrowedTo={turn.narrowedTo}
                      onAsk={(repos) => actions?.onNarrow(i, repos)}
                      readOnly={!actions}
                    />
                  ) : (
                    <Clarify
                      candidates={turn.clarification.candidates}
                      chosenIdx={turn.chosenIdx}
                      onChoose={(idx) => actions?.onChoose(i, idx)}
                      readOnly={!actions}
                    />
                  ))}

                {/* ui-markdown carries the prose typography (index.css), the
                    same block ../loom uses. The measure stays capped here:
                    rongo's answer column is wider than loom's rail.

                    streaming is what the text fade keys on (markdown.tsx):
                    text still arriving fades in, and the class comes off with
                    the done event. Nothing else marks a streaming answer — the
                    caret it once drew is gone. */}
                {turn.text && (
                  <div
                    className={`ui-markdown mt-4 max-w-[68ch]${turn.done ? "" : " streaming"}`}
                  >
                    <Markdown
                      text={turn.text}
                      onMarkerHover={onMarkerHover}
                      // Every turn, from its own list: the pane shows only
                      // the newest, and a tablet has no pane at all.
                      onMarkerOpen={onMarkerOpen}
                      // Known once the turn is done: the citations event is
                      // the last thing before done, so a finished turn with
                      // none has none.
                      backed={backed}
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
                        onClick={() => actions.onRetry(i)}
                        className="rounded-full border border-border bg-panel px-3.5 py-1.5 text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active disabled:opacity-50"
                      >
                        Retry
                      </button>
                    )}
                  </div>
                )}

                {/* What this turn did to the reader's memory, under the
                    answer it applies to. Undo only where there are actions:
                    a shared page never carries it anyway. */}
                {turn.memory && <MemoryChip memory={turn.memory} readOnly={!actions} />}

                {/* The way into the pane, under every answer that cites. The
                    pane lists one turn at a time, and this chip is what points
                    it at THIS one: the reader clicks "6 sources" under an older
                    answer and gets that answer's six, not the newest turn's.
                    Expanded means the pane is open on this turn, not merely
                    open.

                    Only from `xl`, where the pane exists at all. Narrower than
                    that the markers in the text are the way to a source. No
                    aria-controls: the pane is unmounted while shut, and the
                    attribute would name an element that is not there. */}
                {onToggleSources && turn.citations.length > 0 && (
                  <div className="mt-4 hidden xl:block">
                    <button
                      type="button"
                      aria-expanded={sourcesExpanded}
                      onClick={() => onToggleSources(i)}
                      className="inline-flex items-center gap-2 rounded-full border border-border bg-panel px-3.5 py-1.5 text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active"
                    >
                      <span className="font-mono text-xs text-accent-strong">{turn.citations.length}</span>
                      Sources
                    </button>
                  </div>
                )}

                {/* What to ask next, under the answer that prompted it and
                    above the actions on it. Only on the newest turn: an older
                    answer's offers are spent, and a card or a failed turn
                    below means the reader's move is there, not here.

                    Never ochre — ochre is "your move", and nothing here is
                    waiting on the reader. */}
                {actions && isLast && turn.done && turn.followups.length > 0 && (
                  <nav aria-label="Follow-up questions" className="mt-4 flex flex-wrap gap-2">
                    {turn.followups.map((q) => (
                      <button
                        key={q}
                        type="button"
                        disabled={busy}
                        onClick={() => actions.onFollowup(i, q)}
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
                    paid for its gates, and the thread total counts them.

                    Re-explain needs sources besides: a turn answered by a
                    template (nothing found, no commits, a rule kept) has none,
                    and offering it would answer "the sources are no longer
                    indexed" about sources that never were. Copy stays. */}
                {actions && turn.done && (turn.usage || (turn.messageId && !turn.error && !turn.clarification)) && (
                  <div className="mt-4">
                    <div className="flex flex-wrap items-center gap-2">
                      {turn.messageId && !turn.error && !turn.clarification && (
                        <>
                          {!turn.sourceless && (
                            <button
                              type="button"
                              disabled={busy}
                              onClick={() => actions.onReexplain(i)}
                              className="rounded-full border border-border bg-panel px-3.5 py-1.5 text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active disabled:opacity-50"
                            >
                              {turn.audience === "dev" ? "Explain as Analyst" : "Explain as Developer"}
                            </button>
                          )}
                          <button
                            type="button"
                            onClick={() => onCopy(i)}
                            className="rounded-full border border-border bg-panel px-3.5 py-1.5 text-[13.5px] text-ink-dim hover:border-elevated-border hover:bg-active"
                          >
                            {copied ? "Copied" : "Copy as Markdown"}
                          </button>
                          {feedback}
                        </>
                      )}
                      {turn.usage && (
                        <button
                          type="button"
                          aria-label={`Token stats of turn ${i + 1}`}
                          onClick={() => actions.onOpenStats(i)}
                          className={
                            pill +
                            // No chevron: this opens a pane beside the thread
                            // rather than unfolding under itself, and a
                            // chevron would promise the old behaviour.
                            " ml-auto inline-flex items-center gap-1.5 bg-active font-mono text-faint hover:text-muted"
                          }
                        >
                          {tokens(turn.usage.total_tokens)}
                          {turn.usage.cost_usd != null && (
                            <>
                              <span className="opacity-50">·</span>
                              {money(turn.usage.cost_usd)}
                            </>
                          )}
                        </button>
                      )}
                    </div>
                  </div>
                )}
                  </>
                )}
    </div>
  );
}

export default memo(TurnAttempt);
