import { money, tokens, type ThreadTotal } from "./turns";

/**
 * The whole thread's figure beside the title: the app's header and the shared
 * page draw the same one, so a restyle lands in both or neither.
 *
 * Out of sight below sm: the running total beside a serif title does not fit
 * 360px. In the app the same figure is one click from every turn's own pill;
 * on a shared page there is no pill at all, and a phone reader goes without.
 *
 * `onOpen` is what separates the two callers. The app passes it and the badge
 * becomes the way into the thread's stats; the shared page passes none and it
 * stays a label, because a share carries the total and nothing behind it.
 */
export default function ThreadUsageBadge({ total, onOpen }: { total: ThreadTotal; onOpen?: () => void }) {
  const figures = (
    <>
      thread <span className="text-muted">{tokens(total.tokens)}</span>
      {total.cost != null && (
        <>
          <span className="mx-1.5 opacity-50">·</span>
          <span className="text-muted">{money(total.cost)}</span>
        </>
      )}
    </>
  );
  const shape = "ml-2 hidden shrink-0 whitespace-nowrap font-mono text-xs text-faint sm:inline-block";
  if (!onOpen) {
    return (
      <span aria-label="Thread usage" className={shape}>
        {figures}
      </span>
    );
  }
  return (
    <button type="button" aria-label="Thread token stats" onClick={onOpen} className={shape + " hover:text-muted"}>
      {figures}
    </button>
  );
}
