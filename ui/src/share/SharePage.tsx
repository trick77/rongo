import { useEffect, useState } from "react";

import SourceView, { type SourceRef } from "../SourceView";
import ThreadView, { SourcesPane, paneAudienceTurn, sourceTurnOf } from "../ThreadView";
import ThreadUsageBadge from "../ThreadUsageBadge";
import {
  linkChosenCandidates,
  storedRetries,
  storedTurn,
  type Message,
  type ThreadTotal,
  type Turn,
} from "../turns";

/**
 * A shared thread, as someone without a rongo account sees it.
 *
 * Mounted instead of the app rather than inside it (see main.tsx): the app's
 * session gate redirects a 401 to the identity provider, and the one audience
 * this page exists for has no session at all.
 *
 * It is the record and nothing else. No rail, no composer, no account row, and
 * ThreadView is handed no actions — so there is no Retry, no Explain as, no
 * Copy as Markdown, no follow-up chip, and no per-turn usage or cost. The
 * server does not send those last two either; this is the second half of the
 * same rule, not the whole of it. What the thread cost as a whole is the one
 * figure that does arrive, and it sits in the header as it does in the app.
 */
type State =
  | { s: "loading" }
  | { s: "gone" }
  | { s: "failed" }
  | { s: "ready"; title: string; turns: Turn[]; usage: ThreadTotal | null };

export default function SharePage({ token }: { token: string }) {
  const [state, setState] = useState<State>({ s: "loading" });
  const [hot, setHot] = useState<number | null>(null);
  const [viewing, setViewing] = useState<SourceRef | null>(null);
  // The same rule the answering page follows: untouched, the pane answers to
  // the audience of the turn it would show, and the reader's own click wins
  // from then on. A share is one thread and never changes, so nothing resets
  // it. The audience is on the wire — handlePublicShare drops the follow-ups,
  // the per-turn usage and the timeline, and nothing else.
  const [sourcesOpen, setSourcesOpen] = useState<boolean | null>(null);
  // The turn the reader pointed the pane at from the chip under it; null is
  // the newest citing turn. Nothing resets it: a share never gains a turn.
  const [sourceTurn, setSourceTurn] = useState<number | null>(null);

  // Belt and braces with the X-Robots-Tag the two public endpoints set: a
  // crawler that reaches the page rather than the API sees this one. Removed
  // on unmount so a tab that navigates away is not left marked.
  useEffect(() => {
    const meta = document.createElement("meta");
    meta.name = "robots";
    meta.content = "noindex, nofollow";
    document.head.appendChild(meta);
    return () => {
      meta.remove();
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch(`/api/shares/${encodeURIComponent(token)}`);
        if (cancelled) return;
        // Revoked, mistyped and deleted all arrive as the same 404 — the
        // server refuses to tell them apart — so the page does not either.
        if (res.status === 404) {
          setState({ s: "gone" });
          return;
        }
        if (!res.ok) {
          setState({ s: "failed" });
          return;
        }
        const body = (await res.json()) as {
          title: string;
          messages: Message[];
          total_tokens?: number;
          cost_usd?: number;
        };
        if (cancelled) return;
        const list = body.messages ?? [];
        setState({
          s: "ready",
          title: body.title,
          // The same three passes the app runs over a stored thread: retries
          // and re-explains fold under the question they belong to, and a
          // card shows which candidate was chosen.
          turns: storedRetries(linkChosenCandidates(list, list.map(storedTurn))),
          // No total means the thread paid for nothing: show nothing, not a
          // zero. A total without a cost means no call was priced.
          usage: body.total_tokens == null ? null : { tokens: body.total_tokens, cost: body.cost_usd ?? null },
        });
      } catch {
        if (!cancelled) setState({ s: "failed" });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [token]);

  // The tab is named after the thread, so a reader with several links open can
  // tell them apart.
  useEffect(() => {
    if (state.s === "ready" && state.title) document.title = `${state.title} · Rongo`;
  }, [state]);

  if (state.s !== "ready") {
    return (
      <div className="grid h-dvh place-items-center px-6 text-center">
        <div className="max-w-[44ch]">
          {state.s === "loading" && <p className="text-muted">Opening the thread …</p>}
          {state.s === "gone" && (
            <>
              <h1 className="font-serif text-[22px] font-medium leading-tight text-ink">
                This link is no longer available.
              </h1>
              <p className="mt-2 text-muted">
                It may have been revoked, or the thread it pointed at was deleted.
              </p>
            </>
          )}
          {state.s === "failed" && (
            <p role="alert" className="text-accent-strong">
              The thread could not be loaded. Try again in a moment.
            </p>
          )}
        </div>
      </div>
    );
  }

  const showSources = sourcesOpen ?? paneAudienceTurn(state.turns)?.audience === "dev";
  const listedTurn = sourceTurn ?? sourceTurnOf(state.turns);
  // As in Ask: the chip shuts the pane only when it is open on that very
  // turn; from any other turn it moves the pane there.
  const toggleSources = (i: number) => {
    if (showSources && i === listedTurn) {
      setSourcesOpen(false);
      return;
    }
    setSourceTurn(i);
    setSourcesOpen(true);
  };

  return (
    // The app's own shell: a 56px header over the thread, and the Sources
    // column where there is room for it. The chrome stays English — the
    // answers keep the language they were written in, as everywhere else.
    <div className="grid h-dvh grid-rows-[56px_1fr] [@media(max-height:500px)]:grid-rows-[44px_1fr]">
      <header className="grid grid-cols-[auto_1fr_auto] items-center border-b border-border bg-panel">
        <div className="flex h-full items-center gap-2.5 px-2 lg:px-5">
          <span className="sr-only font-serif text-[21px] leading-7 font-medium text-wordmark sm:not-sr-only">
            Rongo
          </span>
        </div>
        <div className="flex min-w-0 items-baseline gap-2.5 px-2 lg:px-6">
          <h1 className="truncate font-serif text-[19px] font-medium text-accent-strong">{state.title}</h1>
          {/* The app's own badge, in the app's own place. */}
          {state.usage && <ThreadUsageBadge total={state.usage} />}
        </div>
        {/* Says what this page is, and by saying "read-only" says why there is
            nothing on it to press. */}
        <div className="flex items-center px-2 lg:px-5">
          <span className="rounded-full bg-active px-2.5 py-0.5 text-xs whitespace-nowrap text-muted">
            Shared · read-only
          </span>
        </div>
      </header>

      <div
        className={
          "grid min-h-0 grid-cols-1" +
          (showSources ? " xl:grid-cols-[1fr_300px] 2xl:grid-cols-[1fr_340px]" : "")
        }
      >
        {/* The same edges the answering column has, and both of them here:
            there is no composer under a share, so the foot runs into the
            window rather than into a gradient someone else already paints.
            Each strip is the padding on its own side, so text clears the fade
            at rest. */}
        <div className="relative min-h-0 min-w-0">
          <div className="thin-scroll h-full overflow-auto">
            <div className="mx-auto max-w-[900px] px-4 pt-5 pb-8 sm:px-6 lg:px-10 lg:pt-8 lg:pb-10">
              <ThreadView
                turns={state.turns}
                actions={null}
                onOpenSource={setViewing}
                onHot={setHot}
                sourceTurn={listedTurn}
                sourcesOpen={showSources}
                onToggleSources={toggleSources}
                threadKey={token}
              />
            </div>
          </div>
          {/* Both strips AFTER the scroller: at z-10 they tie with the diagram
              toolbar inside it, and a tie is settled by tree order. */}
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-x-0 top-0 z-10 h-5 bg-gradient-to-b from-bg to-transparent lg:h-8"
          />
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-x-0 bottom-0 z-10 h-8 bg-gradient-to-t from-bg to-transparent lg:h-10"
          />
        </div>
        {showSources && (
          <SourcesPane
            turns={state.turns}
            sourceTurn={listedTurn}
            hot={hot}
            onOpen={setViewing}
            onClose={() => setSourcesOpen(false)}
          />
        )}
      </div>

      {/* The share's own endpoint, never /api/source: that one takes any
          repo/path/sha and would be a reader for the whole indexed corpus. */}
      {viewing && (
        <SourceView
          source={viewing}
          endpoint={`/api/shares/${encodeURIComponent(token)}/source`}
          onClose={() => setViewing(null)}
        />
      )}
    </div>
  );
}
