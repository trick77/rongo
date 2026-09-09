import { money, tokens, type ThreadTotal } from "./turns";

/**
 * The whole thread's figure beside the title: the app's header and the shared
 * page draw the same one, so a restyle lands in both or neither.
 *
 * Out of sight below sm: the running total beside a serif title does not fit
 * 360px. In the app the same figure sits in every turn's own usage block; on
 * a shared page there is no such block, and a phone reader goes without.
 */
export default function ThreadUsageBadge({ total }: { total: ThreadTotal }) {
  return (
    <span
      aria-label="Thread usage"
      className="ml-2 hidden shrink-0 whitespace-nowrap font-mono text-xs text-faint sm:inline-block"
    >
      thread <span className="text-muted">{tokens(total.tokens)}</span>
      {total.cost != null && (
        <>
          <span className="mx-1.5 opacity-50">·</span>
          <span className="text-muted">{money(total.cost)}</span>
        </>
      )}
    </span>
  );
}
