import { useEffect, useRef, useState } from "react";
import { Chevron } from "./icons";

/** One repository the question matched, as the SSE event and the stored
 * thread both send it. The panel has no titles and no summaries: nothing was
 * named by a model, because the names were already known. */
export type NarrowRepo = {
  repo: string;
  branch: string;
};

/** How many repositories the reader may take at once. The same number the
 * backend enforces (maxNarrowRepos): each one costs its own search at full
 * depth, and the fused result is still cut to one comparison's worth, so a
 * fourth side competes for room rather than adding any. */
const maxPicked = 3;

/**
 * The panel a turn ends with when the question fits more repositories than a
 * card can offer.
 *
 * It is not a card, and deliberately so. A card names two to five candidates a
 * model wrote titles for and takes one answer. This names every repository that
 * matched, in the index's own words, and takes up to three — because past four
 * a card would show four and never mention the rest, and answering across all
 * of them spreads one search over twenty repositories and answers none.
 *
 * There is no "all repositories" way out for that same reason. The only way on
 * is to narrow, here or by asking again.
 *
 * Ochre while open — that colour means "your move" — and dropped once the turn
 * went on. Like the card it stays in the thread afterwards, collapsed to what
 * it narrowed to: the answer below it only reads correctly beside the
 * repositories it was answered from.
 */
export default function Narrow({
  repos,
  narrowedTo = null,
  onAsk,
}: {
  repos: NarrowRepo[];
  narrowedTo?: string[] | null;
  onAsk: (repos: string[]) => void;
}) {
  const decided = narrowedTo != null && narrowedTo.length > 0;
  const [open, setOpen] = useState(!decided);
  const [picked, setPicked] = useState<string[]>([]);

  // Collapses the instant the turn goes on, and opens again if that turn
  // failed and the narrowing was taken back — the panel is "your move" once
  // more. Only fires on a transition, so a reader who toggles it by hand is
  // never fought.
  const prevDecided = useRef(decided);
  useEffect(() => {
    if (!prevDecided.current && decided) setOpen(false);
    if (prevDecided.current && !decided) setOpen(true);
    prevDecided.current = decided;
  }, [decided]);

  const toggle = (repo: string) =>
    setPicked((p) => (p.includes(repo) ? p.filter((r) => r !== repo) : [...p, repo]));

  return (
    <div className={"mt-4 rounded-ui border bg-panel " + (decided ? "border-border" : "border-ochre")}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-3 px-4 py-3 text-left text-[14.5px]"
      >
        <Chevron open={open} />
        {decided ? (
          <>
            <span>Narrowed to</span>
            <span className="font-mono text-[12.5px] font-medium text-ochre">
              {narrowedTo!.join(", ")}
            </span>
          </>
        ) : (
          <span className="font-medium text-ochre">That is too broad to ask about.</span>
        )}
      </button>

      {open && (
        <div className="px-4 pb-4">
          <p className="m-0 mb-3 max-w-[70ch] text-sm text-muted">
            <span className="font-medium text-ink-dim">{repos.length} repositories</span> match this
            question about equally well. Pick the ones you meant — at most {maxPicked} — or ask
            again with a repository name in your question.
          </p>
          <ul className="m-0 mb-3 flex list-none flex-wrap gap-2 p-0">
            {repos.map((r) => {
              const on = picked.includes(r.repo);
              // Full once three are picked: the ones already taken stay live
              // so one can be swapped out, and the rest go quiet rather than
              // silently doing nothing when clicked.
              const full = !on && picked.length >= maxPicked;
              return (
                <li key={r.repo}>
                  <button
                    type="button"
                    onClick={() => toggle(r.repo)}
                    disabled={decided || full}
                    aria-pressed={on}
                    className={
                      "flex items-center gap-2 rounded-ui-sm border px-2.5 py-1 font-mono text-[11.5px] " +
                      (on
                        ? "border-accent bg-accent-dim text-ink"
                        : "border-elevated-border bg-elevated text-ink-dim") +
                      (decided || full ? " opacity-40" : " hover:border-accent")
                    }
                  >
                    <span>{r.repo}</span>
                    <span className="text-faint">{r.branch}</span>
                  </button>
                </li>
              );
            })}
          </ul>
          {!decided && (
            <div className="flex items-center gap-3">
              <button
                type="button"
                disabled={picked.length === 0}
                onClick={() => onAsk(picked)}
                className={
                  "rounded-ui-sm px-3 py-1.5 text-[13.5px] font-medium " +
                  (picked.length === 0
                    ? "cursor-default border border-border bg-bg text-faint"
                    : "bg-accent-fill text-white")
                }
              >
                Ask {picked.length > 0 ? `these ${picked.length}` : "these"}
              </button>
              {picked.length >= maxPicked && (
                <span className="text-[12.5px] text-ochre">
                  Maximum reached — deselect one to swap.
                </span>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
