import { useEffect, useRef, useState } from "react";
import { Chevron } from "./icons";

/** One entry on the clarification card, as the SSE event and the stored
 * thread both send it (a stored candidate may also carry module_key, which
 * this view has no use for). */
export type ClarifyCandidate = {
  idx: number;
  title: string;
  summary: string;
  repo: string;
  branch: string;
};

/**
 * The clarification card: the turn ended by asking which mechanism was
 * meant. Ochre while open — that colour means "your move" — and lost for
 * good the moment a choice is made, even if the card is reopened later.
 *
 * The decision belongs to the thread's record, not to a dialog: after a
 * choice the card collapses to one line but never disappears, and reopening
 * it still names every candidate with the chosen one marked. It is a record
 * of what was decided, not a second chance to decide it — one card, one
 * answer, so the candidates are inert from then on and a correction is a new
 * question. The backend refuses a second resume with the same card too.
 */
export default function Clarify({
  candidates,
  chosenIdx = null,
  onChoose,
  readOnly = false,
}: {
  candidates: ClarifyCandidate[];
  chosenIdx?: number | null;
  onChoose: (idx: number) => void;
  /**
   * On a shared page the card is a record and nothing more: the candidates are
   * inert, and it never wears the ochre. Ochre means "your move", and a reader
   * following a link has none — the choice was, and stays, the owner's.
   */
  readOnly?: boolean;
}) {
  const [open, setOpen] = useState(chosenIdx == null);
  // Collapses the instant a choice lands, and opens again if that choice is
  // taken back because the turn it started failed — the card is "your move"
  // once more. Neither fights a reader who toggles it by hand: the effect
  // only fires on a transition, never on a re-render that leaves chosenIdx
  // unchanged.
  const prevChosen = useRef(chosenIdx);
  useEffect(() => {
    if (prevChosen.current == null && chosenIdx != null) setOpen(false);
    if (prevChosen.current != null && chosenIdx == null) setOpen(true);
    prevChosen.current = chosenIdx;
  }, [chosenIdx]);

  const chosen = chosenIdx != null ? candidates.find((c) => c.idx === chosenIdx) : undefined;

  return (
    <div
      className={
        "mt-4 rounded-ui border bg-panel " +
        (chosenIdx == null && !readOnly ? "border-ochre" : "border-border")
      }
    >
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-3 px-4 py-3 text-left text-[14.5px]"
      >
        <Chevron open={open} />
        {chosenIdx == null ? (
          readOnly ? (
            // Not a question here: nobody on this page can answer it, and
            // asking anyway would leave a reader looking for a button.
            <span className="text-muted">Asked back: which one was meant</span>
          ) : (
            <span className="font-medium text-ochre">Which one do you mean?</span>
          )
        ) : (
          <>
            {chosen?.repo && (
              <span className="font-mono text-[12.5px] font-medium text-ochre">{chosen.repo}</span>
            )}
            <span>Chosen: {chosen?.title}</span>
            {chosen?.branch && (
              <span className="ml-auto font-mono text-[11.5px] text-faint">{chosen.branch}</span>
            )}
          </>
        )}
      </button>

      {open && (
        <ul className="grid gap-2 px-4 pb-4">
          {candidates.map((c) => (
            <li key={c.idx} className={c.repo ? undefined : "mt-1 border-t border-border pt-3"}>
              <button
                type="button"
                onClick={() => onChoose(c.idx)}
                disabled={chosenIdx != null || readOnly}
                aria-pressed={c.idx === chosenIdx}
                className={
                  "w-full rounded-ui-sm border border-l-[3px] bg-bg py-3 pr-3.5 pl-3 text-left " +
                  (chosenIdx == null && !readOnly ? "hover:border-accent " : "") +
                  (c.idx === chosenIdx
                    ? "border-accent ring-1 ring-accent"
                    : "border-border " + (c.repo ? "border-l-accent-dim" : ""))
                }
              >
                {/* The repository leads, on a line of its own. It is what
                    tells two candidates of the same shape apart, and after
                    the title it read as a footnote to it rather than as the
                    thing being chosen between.

                    An entry with no repository is the card's "all
                    repositories" choice: it stands for every one of them, so
                    the line is left out entirely rather than printed empty. */}
                {c.repo && (
                  <div className="mb-1.5 flex items-center gap-2">
                    <span className="font-mono text-[12.5px] font-medium text-ochre">{c.repo}</span>
                    <span className="font-mono text-[11.5px] text-faint">{c.branch}</span>
                    <span aria-hidden="true" className="h-px flex-1 bg-border-soft" />
                  </div>
                )}
                <div className="flex items-center gap-2">
                  <span className="font-medium text-ink">{c.title}</span>
                  {c.idx === chosenIdx && (
                    <span className="ml-auto text-xs font-medium text-accent-strong">Chosen</span>
                  )}
                </div>
                <p className="mt-1 text-sm text-muted">{c.summary}</p>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
