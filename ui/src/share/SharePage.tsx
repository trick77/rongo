import { useEffect, useState } from "react";

import SourceView, { type SourceRef } from "../SourceView";
import ThreadView, { SourcesPane } from "../ThreadView";
import { linkChosenCandidates, storedRetries, storedTurn, type Message, type Turn } from "../turns";
import logo from "../assets/rongo-wide.png";

/**
 * A shared thread, as someone without a rongo account sees it.
 *
 * Mounted instead of the app rather than inside it (see main.tsx): the app's
 * session gate redirects a 401 to the identity provider, and the one audience
 * this page exists for has no session at all.
 *
 * It is the record and nothing else. No rail, no composer, no account row, and
 * ThreadView is handed no actions — so there is no Retry, no Explain as, no
 * Copy as Markdown, no follow-up chip, and no usage or cost anywhere. The
 * server does not send those last two either; this is the second half of the
 * same rule, not the whole of it.
 */
type State =
  | { s: "loading" }
  | { s: "gone" }
  | { s: "failed" }
  | { s: "ready"; title: string; turns: Turn[] };

export default function SharePage({ token }: { token: string }) {
  const [state, setState] = useState<State>({ s: "loading" });
  const [hot, setHot] = useState<number | null>(null);
  const [viewing, setViewing] = useState<SourceRef | null>(null);

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
        const body = (await res.json()) as { title: string; messages: Message[] };
        if (cancelled) return;
        const list = body.messages ?? [];
        setState({
          s: "ready",
          title: body.title,
          // The same three passes the app runs over a stored thread: retries
          // and re-explains fold under the question they belong to, and a
          // card shows which candidate was chosen.
          turns: storedRetries(linkChosenCandidates(list, list.map(storedTurn))),
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
    if (state.s === "ready" && state.title) document.title = `${state.title} · rongo`;
  }, [state]);

  if (state.s !== "ready") {
    return (
      <div className="grid h-dvh place-items-center px-6 text-center">
        <div className="max-w-[44ch]">
          <span
            aria-hidden="true"
            className="mx-auto mb-5 block h-[38px] w-[38px] rounded-lg bg-accent-fill bg-[length:auto_30px] bg-[4px_center] bg-no-repeat bg-blend-luminosity"
            style={{ backgroundImage: `url(${logo})` }}
          />
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

  return (
    // The app's own shell: a 56px header over the thread, and the Sources
    // column where there is room for it. The chrome stays English — the
    // answers keep the language they were written in, as everywhere else.
    <div className="grid h-dvh grid-rows-[56px_1fr] [@media(max-height:500px)]:grid-rows-[44px_1fr]">
      <header className="grid grid-cols-[auto_1fr_auto] items-center border-b border-border bg-panel">
        <div className="flex h-full items-center gap-2.5 px-2 lg:px-5">
          <span
            aria-hidden="true"
            className="h-[30px] w-[30px] shrink-0 rounded-lg bg-accent-fill bg-[length:auto_24px] bg-[3px_center] bg-no-repeat bg-blend-luminosity"
            style={{ backgroundImage: `url(${logo})` }}
          />
          <span className="sr-only font-serif text-[21px] font-semibold tracking-tight text-accent-strong sm:not-sr-only">
            rongo
          </span>
        </div>
        <div className="flex min-w-0 items-baseline gap-2.5 px-2 lg:px-6">
          <h1 className="truncate font-serif text-[19px] font-medium text-accent-strong">{state.title}</h1>
        </div>
        {/* Says what this page is, and by saying "read-only" says why there is
            nothing on it to press. */}
        <div className="flex items-center px-2 lg:px-5">
          <span className="rounded-full bg-active px-2.5 py-0.5 text-xs whitespace-nowrap text-muted">
            Shared · read-only
          </span>
        </div>
      </header>

      <div className="grid min-h-0 grid-cols-1 xl:grid-cols-[1fr_300px] 2xl:grid-cols-[1fr_340px]">
        <div className="min-h-0 min-w-0 overflow-auto">
          <div className="max-w-[900px] px-4 pt-5 pb-8 sm:px-6 lg:px-10 lg:pt-8 lg:pb-10">
            <ThreadView
              turns={state.turns}
              actions={null}
              onOpenSource={setViewing}
              onHot={setHot}
              threadKey={token}
            />
          </div>
        </div>
        <SourcesPane turns={state.turns} hot={hot} onOpen={setViewing} />
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
