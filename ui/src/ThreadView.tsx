import { useEffect, useLayoutEffect, useMemo, useRef, useState, useCallback } from "react";
import Question from "./Question";
import PasteChip from "./PasteChip";
import { strip } from "./pastes";
import TurnAttempt from "./TurnAttempt";
import { CheckIcon, CopyIcon } from "./icons";
import {
  clock,
  commitDay,
  groupByQuestion,
  languages,
  pill,
  roleName,
  shortSha,
  stageLabel,
  type Citation,
  type Turn,
} from "./turns";
import { isCommit } from "./SourceView";

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
 * What the view remembers about itself — which failure is unfolded and which
 * turn has just been copied — lives here rather
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
  // The too-broad panel's move: the repositories the reader picked off it.
  onNarrow: (i: number, repos: string[]) => void;
  // Opens the stats pane on this turn. The pill used to unfold a table here;
  // it now opens the pane, which holds the same ledger and everything the
  // table had nowhere to put — what was cached, how long each call took, and
  // what filled the answer's context.
  onOpenStats: (i: number) => void;
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
   * The turn the sources pane lists, whether the pane is on screen, and the
   * way to turn it round from a given turn. All the caller's: the pane is a
   * cell of ITS grid, while the chip that opens it is a row inside a turn, so
   * the page is the only thing that sees both ends. Left out — a test — the
   * chip is not rendered at all and the markers in the text are the only way
   * to a source, which is what happens below `xl` anyway.
   */
  sourceTurn?: number;
  sourcesOpen?: boolean;
  onToggleSources?: (turnIndex: number) => void;
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

/**
 * The turn whose audience decides whether the pane is open: the newest citing
 * turn, or the one still being written if there is one.
 *
 * The running turn has to count. Citations arrive after the last token, so a
 * fresh Developer question would leave this at the PREVIOUS turn — an Analyst
 * one, most likely — for the whole stream, and the pane would snap in on the
 * final frame, narrowing the column the reader is mid-sentence in. Opening it
 * at the start costs an empty pane saying what will appear there, which is
 * what that empty state is for.
 */
export function paneAudienceTurn(turns: Turn[]): Turn | undefined {
  const last = turns[turns.length - 1];
  if (last && !last.done) return last;
  return turns[sourceTurnOf(turns)];
}

export default function ThreadView({
  turns,
  busy = false,
  actions = null,
  onOpenSource,
  onHot,
  sourceTurn,
  sourcesOpen = false,
  onToggleSources,
  threadKey = null,
}: ThreadViewProps) {
  const [copied, setCopied] = useState<number | null>(null);
  // The question copied, by the index of the turn it was asked in. Its own
  // state: copying the question and copying the answer are two controls, and
  // one flag would light both.
  const [copiedQuestion, setCopiedQuestion] = useState<number | null>(null);
  // The superseded failures the reader has unfolded. A failure stays in the
  // record and stays on the page, but a turn that went on to answer should
  // not open with the attempt that broke — so it folds to a line, and the
  // line says what it is.
  const [openFailure, setOpenFailure] = useState<Set<number>>(new Set());
  // Everything handed to a turn is held steady across renders: a turn is
  // memoized (TurnAttempt), and one fresh function among its props would have
  // every turn drawn again for every streamed token.
  const toggleFailure = useCallback((i: number) => {
    setOpenFailure((prev) => {
      const next = new Set(prev);
      if (!next.delete(i)) next.add(i);
      return next;
    });
  }, []);

  // What the caller passed on its newest render, for the steady functions
  // below to call through to. The callers' own handlers are fresh arrows.
  const latest = useRef({ actions, onOpenSource, onToggleSources });
  useLayoutEffect(() => {
    latest.current = { actions, onOpenSource, onToggleSources };
  });
  const toggleSources = useCallback((i: number) => latest.current.onToggleSources?.(i), []);

  // Another thread: the indices this view is holding mean something else now.
  useEffect(() => {
    setOpenFailure(new Set());
    setCopied(null);
    setCopiedQuestion(null);
  }, [threadKey]);

  // The "Copied" feedback times out through these, cleared on unmount so a
  // reader who leaves within the moment does not have state set on a view
  // that is gone.
  const copyTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const copyQuestionTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (copyTimer.current) clearTimeout(copyTimer.current);
      if (copyQuestionTimer.current) clearTimeout(copyQuestionTimer.current);
    },
    [],
  );

  // Stable, or the memoized Markdown of the source turn — the previous
  // answer while the next one streams — re-rendered and re-highlighted on
  // every token.
  const hot = useRef(onHot);
  useLayoutEffect(() => {
    hot.current = onHot;
  });
  const setHot = useCallback((marker: number | null) => hot.current?.(marker), []);

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
        if (c) latest.current.onOpenSource(c);
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

  const copy = useCallback(async (turnIndex: number) => {
    const actions = latest.current.actions;
    if (!actions) return;
    // Only on a clipboard that actually took it. In an insecure context, or
    // with the permission refused, the button saying "Copied" would be a
    // plain lie — and the reader would paste whatever was there before.
    if (!(await actions.onCopy(turnIndex))) return;
    setCopied(turnIndex);
    if (copyTimer.current) clearTimeout(copyTimer.current);
    copyTimer.current = setTimeout(() => setCopied(null), 1500);
  }, []);

  async function copyQuestion(turnIndex: number) {
    if (!actions) return;
    if (!(await actions.onCopyQuestion(turnIndex))) return;
    setCopiedQuestion(turnIndex);
    if (copyQuestionTimer.current) clearTimeout(copyQuestionTimer.current);
    copyQuestionTimer.current = setTimeout(() => setCopiedQuestion(null), 1500);
  }

  // One article per question. The list itself stays flat — every action here
  // addresses a turn by its position in it — and only the rendering groups.
  const groups = useMemo(() => groupByQuestion(turns), [turns]);

  // The turn the pane lists. Without the caller's word on it, the newest
  // citing turn, which is what the pane shows on its own.
  const sourceTurnIndex = sourceTurn ?? sourceTurnOf(turns);

  // A highlight belongs to the turn the pane shows. When the pane moves to a
  // newer turn, the old Markdown's mouseleave never fires for it.
  useEffect(() => {
    onHot?.(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourceTurnIndex]);

  return (
    <>
            {groups.map((group, g) => {
              const asked = turns[group[0]];
              // The typed words, with the pastes peeled off the tail to be
              // drawn as chips. A block that is not where the fold put it
              // stays in the prose and gets no chip.
              const { typed, matched } = strip(asked.question, asked.pastes);
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
                {typed && <Question text={typed} />}
                {/* Each paste folded to a line under the words, the way it
                    stood in the composer. Folded: the reader knows what
                    they pasted, and the answer is what they came for. */}
                {matched.some(Boolean) && (
                  <div className="mt-2 flex max-w-[68ch] flex-wrap gap-1.5 border-l-2 border-elevated pl-4">
                    {asked.pastes.map((p, i) =>
                      matched[i] ? <PasteChip key={i} text={p.text} lines={p.lines} /> : null,
                    )}
                  </div>
                )}
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
                    <TurnAttempt
                      key={i}
                      turn={turn}
                      i={i}
                      label={group.length > 1 ? stageLabel(turn, firstAnswer) : null}
                      superseded={superseded}
                      folded={folded}
                      busy={busy}
                      actions={actions}
                      isLast={i === turns.length - 1}
                      onMarkerHover={i === sourceTurnIndex ? setHot : undefined}
                      // Every turn, from its own list: the pane shows only
                      // the newest, and a tablet has no pane at all.
                      onMarkerOpen={openMarker(i)}
                      // Known once the turn is done: the citations event is
                      // the last thing before done, so a finished turn with
                      // none has none.
                      backed={backedMarkers(turn)}
                      sourcesExpanded={sourcesOpen && i === sourceTurnIndex}
                      onToggleSources={onToggleSources ? toggleSources : undefined}
                      copied={copied === i}
                      onCopy={copy}
                      onToggleFailure={toggleFailure}
                    />
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
  sourceTurn,
  hot,
  onOpen,
  onClose,
}: {
  turns: Turn[];
  /** The turn to list. Left out, the newest citing one. */
  sourceTurn?: number;
  hot: number | null;
  onOpen: (c: Citation) => void;
  /** Shuts the pane. The caller owns whether it is on screen — this only says
   * that the reader asked for it to go. */
  onClose?: () => void;
}) {
  const sourceTurnIndex = sourceTurn ?? sourceTurnOf(turns);
  const listed = sourceTurnIndex >= 0 ? turns[sourceTurnIndex] : null;
  const groups = useMemo(() => groupByQuestion(turns), [turns]);
  return (
    <aside aria-label="Sources" className="hidden min-h-0 flex-col border-l border-border bg-panel xl:flex">
      <header className="flex items-center border-b border-border px-4.5 py-3.5 text-[11px] font-medium uppercase tracking-[.12em] text-faint">
        Sources
        {listed && (
          <span className="ml-auto font-mono tracking-normal">
            {/* The turn the reader sees, counted in questions like the pill
                on the article — not the row's place in the record. */}
            turn {groups.findIndex((g) => g.includes(sourceTurnIndex)) + 1} · {listed.citations.length}
          </span>
        )}
        {/* The same close the source viewer and the diagram draw: a × at the
            end of the header. ml-auto only when the count is not there to
            carry it — with both holding it, flexbox splits the free space and
            the count drifts away from the button. */}
        {onClose && (
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className={
              "-my-1 grid h-8 w-8 place-items-center rounded-ui-sm text-lg leading-none text-muted hover:bg-active hover:text-ink" +
              (listed ? " -mr-1" : " -mr-1 ml-auto")
            }
          >
            ×
          </button>
        )}
      </header>
      <div className="min-h-0 flex-1 overflow-auto">
        {!listed && (
          <p className="px-4.5 py-4 text-[13px] text-faint">
            The files an answer was written from appear here, numbered like the markers in the text.
          </p>
        )}
        {listed?.citations.map((c) => (
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
            {isCommit(c) ? (
              // A commit row: the short sha and the day in mono, the subject
              // as the thing to open. No path, because the change is the
              // evidence, not any one file it touched.
              <span className="text-xs break-words text-muted">
                <span className="font-mono">
                  {shortSha(c.sha)} · {commitDay(c)}
                </span>
                {" · "}
                <b className="font-medium text-ink-dim underline-offset-[3px] group-hover:underline group-hover:decoration-accent">
                  {c.subject}
                </b>
              </span>
            ) : (
              <span className="font-mono text-xs break-all text-muted">
                {c.path.includes("/") ? c.path.slice(0, c.path.lastIndexOf("/") + 1) : ""}
                <b className="font-medium text-ink-dim underline-offset-[3px] group-hover:underline group-hover:decoration-accent">
                  {c.path.slice(c.path.lastIndexOf("/") + 1)}
                </b>
                :{c.start_line}-{c.end_line}
              </span>
            )}
          </button>
        ))}
      </div>
    </aside>
  );
}
