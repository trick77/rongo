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
 * choice the card collapses to one line but never disappears, and every
 * candidate — including the one already picked — stays clickable. Picking a
 * different one starts a new turn; nothing here is overwritten.
 */
export default function Clarify({
  candidates,
  chosenIdx = null,
  onChoose,
}: {
  candidates: ClarifyCandidate[];
  chosenIdx?: number | null;
  onChoose: (idx: number) => void;
}) {
  const [open, setOpen] = useState(chosenIdx == null);
  // Collapses the instant a choice lands, without fighting a reader who
  // later reopens it manually — the effect only fires on the null → number
  // transition, never on a re-render that leaves chosenIdx unchanged.
  const prevChosen = useRef(chosenIdx);
  useEffect(() => {
    if (prevChosen.current == null && chosenIdx != null) setOpen(false);
    prevChosen.current = chosenIdx;
  }, [chosenIdx]);

  const chosen = chosenIdx != null ? candidates.find((c) => c.idx === chosenIdx) : undefined;

  return (
    <div
      className={
        "mt-4 rounded-ui border bg-panel " +
        (chosenIdx == null ? "border-ochre" : "border-border")
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
          <span className="font-medium text-ochre">Which one do you mean?</span>
        ) : (
          <>
            <span>Chosen: {chosen?.title}</span>
            <span className="ml-auto font-mono text-[11.5px] text-faint">
              {chosen?.repo} · {chosen?.branch}
            </span>
          </>
        )}
      </button>

      {open && (
        <ul className="grid gap-2 px-4 pb-4">
          {candidates.map((c) => (
            <li key={c.idx}>
              <button
                type="button"
                onClick={() => onChoose(c.idx)}
                aria-pressed={c.idx === chosenIdx}
                className={
                  "w-full rounded-ui-sm border bg-bg px-3.5 py-3 text-left hover:border-accent " +
                  (c.idx === chosenIdx ? "border-accent ring-1 ring-accent" : "border-border")
                }
              >
                <div className="flex items-center gap-2">
                  <span className="font-medium text-ink">{c.title}</span>
                  <span className="font-mono text-[11.5px] text-faint">
                    {c.repo} · {c.branch}
                  </span>
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
