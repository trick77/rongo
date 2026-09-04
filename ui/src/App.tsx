import { useCallback, useEffect, useState } from "react";
import Ask, { money } from "./Ask";
import RepoList, { lastRunAt, relative, type Repo } from "./RepoList";
import Threads, { type Thread } from "./Threads";
import { AskIcon, ReposIcon } from "./icons";
import logo from "./assets/rongo-wide.png";

type Page = "ask" | "repos";

type Me = { subject: string; email: string; is_admin: boolean };

/**
 * The session gate. /api/me is the one request the app makes before it renders
 * anything, so an expired cookie sends the browser to the provider instead of
 * letting every panel fail with its own 401.
 *
 * "checking" is a real state, not a detail: rendering the app first and
 * redirecting afterwards shows a flash of an empty, signed-out UI on every
 * reload.
 *
 * "halted" is what keeps the automatic redirect from becoming a loop. Both the
 * failed callback and a completed sign-out land on a URL that says so, and the
 * gate must offer a button there rather than send the browser back to a
 * provider that will return to the same place.
 */
type Session =
  | { state: "checking" }
  | { state: "in"; me: Me }
  | { state: "out" }
  | { state: "halted"; message: string };

/**
 * A callback that failed and a sign-out that finished both look like "no
 * session" to /api/me, and redirecting on either is a tight loop:
 *
 *   - failed callback: the provider still has a live session, so it answers the
 *     next /api/auth/login without a prompt, and the callback fails the same
 *     way. Two tabs opening the app at once are enough to trigger it — the
 *     second StartLogin overwrites the first tab's state cookie.
 *   - after sign-out: rongo revoked its own session, but the provider's is
 *     untouched and, with consent pre-granted, hands out a fresh token without
 *     the user seeing anything. The button would appear to do nothing.
 */
function haltReason(search: string): string | null {
  const params = new URLSearchParams(search);
  if (params.has("signed_out")) {
    return "Signed out. The session at the provider is still live — sign out there as well to end it.";
  }
  if (params.has("auth_error")) {
    return "Sign-in could not be completed. Try again; if it keeps failing, the reason is in the server log.";
  }
  return null;
}

function useSession(): Session {
  const [session, setSession] = useState<Session>({ state: "checking" });

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const halt = haltReason(window.location.search);
      if (halt !== null) {
        setSession({ state: "halted", message: halt });
        return;
      }
      let res: Response;
      try {
        res = await fetch("/api/me");
      } catch {
        // A network error is not a signed-out session. Redirecting here would
        // bounce the user to the provider every time the connection drops, so
        // the app renders and its panels report their own failures.
        if (!cancelled) setSession({ state: "in", me: { subject: "", email: "", is_admin: false } });
        return;
      }
      if (cancelled) return;
      if (res.status === 401) {
        setSession({ state: "out" });
        // A full navigation, not a fetch: the provider answers with a login
        // page and a redirect chain, neither of which a fetch can follow into
        // the address bar.
        window.location.href = "/api/auth/login";
        return;
      }
      if (!res.ok) {
        // A 500, or the 503 requireAuth answers when auth is unconfigured.
        // Rendering the app here would show a fully chromed, apparently
        // signed-in UI whose every panel then fails on its own.
        setSession({
          state: "halted",
          message: `The session could not be checked (HTTP ${res.status}).`,
        });
        return;
      }
      setSession({ state: "in", me: (await res.json()) as Me });
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  return session;
}

async function logout() {
  try {
    const res = await fetch("/api/auth/logout", { method: "POST" });
    const body = (await res.json()) as { redirect_url?: string };
    window.location.href = body.redirect_url ?? "/?signed_out=1";
  } catch {
    // The cookie may or may not be gone; reloading lets the session gate
    // decide, which is the only place that answer is authoritative.
    window.location.reload();
  }
}

/**
 * threadKey is where the open thread survives a reload. The conversation is a
 * record on the server; this is only the bookmark into it.
 */
const threadKey = "rongo.thread";

// Guarded because Safari's private mode throws on storage access. Losing the
// bookmark is a small annoyance; a blank page instead of the app is not.
function storedThread(): number | null {
  try {
    const raw = localStorage.getItem(threadKey);
    if (!raw) return null;
    const id = Number(raw);
    return Number.isFinite(id) && id > 0 ? id : null;
  } catch {
    return null;
  }
}

function rememberThread(id: number | null) {
  try {
    if (id === null) localStorage.removeItem(threadKey);
    else localStorage.setItem(threadKey, String(id));
  } catch {
    // See storedThread.
  }
}

/**
 * The index line at the foot of the rail: whether the index is current, and
 * when it last ran. Read from /api/repos, the same status the Repos page
 * shows in full; a list that cannot be loaded shows nothing rather than a
 * warning nobody can act on from here.
 */
function useIndexStatus(enabled: boolean, version: number): { ok: boolean; when: string } | null {
  const [status, setStatus] = useState<{ ok: boolean; last: string | null } | null>(null);
  useEffect(() => {
    // Nothing is fetched before the session gate has let the app through:
    // the gate is the one place that decides whether to talk to the server.
    // Re-read whenever the thread list is (every turn), so the line does
    // not freeze at what was true when the tab was opened.
    if (!enabled) return;
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/repos");
        if (!res.ok) return;
        const repos = (await res.json()) as Repo[];
        if (cancelled || !Array.isArray(repos) || repos.length === 0) return;
        // Only repos still being indexed count: a deactivated one keeps its
        // last error in the record, but nobody is going to fix it here.
        const live = repos.filter((r) => r.enabled);
        setStatus({ ok: live.every((r) => !r.last_error), last: lastRunAt(live) });
      } catch {
        // See above.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [enabled, version]);
  // "N min ago" is computed at render, not stored, so it ages with the page.
  return status && { ok: status.ok, when: relative(status.last) };
}

const navItem =
  "relative flex w-full items-center gap-3 rounded-ui-sm px-3 py-2 text-left text-[15px] hover:bg-active hover:text-ink";

export default function App() {
  const [page, setPage] = useState<Page>("ask");
  const [threadId, setThreadId] = useState<number | null>(storedThread);
  // Bumped whenever the list may have changed. The titles are written by the
  // server — a placeholder on Create, the model's version later from a
  // background goroutine — and neither can push.
  const [threadsVersion, setThreadsVersion] = useState(0);
  const [busy, setBusy] = useState(false);
  const [threads, setThreads] = useState<Thread[]>([]);
  // The open thread's running total, as Ask reports it: every turn on
  // screen summed. Shown in the header next to the title.
  const [usageTotal, setUsageTotal] = useState<{ tokens: number; cost: number | null } | null>(null);
  const session = useSession();
  const index = useIndexStatus(session.state === "in", threadsVersion);

  const selectThread = useCallback((id: number | null) => {
    setThreadId(id);
    rememberThread(id);
  }, []);

  const refreshThreads = useCallback(() => setThreadsVersion((v) => v + 1), []);

  // Nothing is rendered until the session is known: the alternative is a flash
  // of the signed-out app on every reload, and a redirect landing on top of it.
  if (session.state !== "in") {
    return (
      <main className="mx-auto max-w-6xl p-8">
        {/*
          Deliberately no <h1>: the app's own heading is how the tests and a
          reader tell "signed in" from "not yet", and repeating it here would
          make the gate screen indistinguishable from the app.
        */}
        <p className="text-sm text-muted">
          {session.state === "checking" && "Checking the session …"}
          {session.state === "out" && "Redirecting to sign-in …"}
          {session.state === "halted" && session.message}
        </p>
        {session.state === "halted" && (
          <a
            href="/api/auth/login"
            className="mt-4 inline-block rounded-full bg-accent-fill px-4 py-2 text-sm font-medium text-ink hover:bg-accent-strong"
          >
            Sign in
          </a>
        )}
      </main>
    );
  }

  const openTitle = threadId === null ? null : (threads.find((t) => t.id === threadId)?.title ?? null);
  const total = threadId === null ? null : usageTotal;

  return (
    <div className="grid h-screen grid-rows-[56px_1fr]">
      <header className="grid grid-cols-[300px_1fr_auto] items-center border-b border-border bg-panel">
        <div className="flex h-full items-center gap-2.5 px-5">
          <span
            aria-hidden="true"
            className="h-[30px] w-[30px] shrink-0 rounded-lg bg-accent-fill bg-[length:auto_24px] bg-[3px_center] bg-no-repeat bg-blend-luminosity"
            style={{ backgroundImage: `url(${logo})` }}
          />
          <h1 className="font-serif text-[21px] font-semibold tracking-tight">rongo</h1>
        </div>
        {/* Baseline, not centre: the usage in small mono sits on the same
            line as the serif title, not floating beside its middle. */}
        <div className="flex min-w-0 items-baseline gap-2.5 px-6 text-muted">
          {page === "ask" ? (
            <>
              <span>Threads</span>
              <span className="text-faint">/</span>
              <span className="truncate font-serif text-[19px] font-medium text-ink">
                {openTitle ?? "New question"}
              </span>
              {total && (
                <span
                  aria-label="Thread usage"
                  className="ml-2 shrink-0 whitespace-nowrap font-mono text-xs text-faint"
                >
                  thread{" "}
                  <span className="text-muted">{total.tokens.toLocaleString("en-GB")} tok</span>
                  {total.cost != null && (
                    <>
                      <span className="mx-1.5 opacity-50">·</span>
                      <span className="text-muted">{money(total.cost)}</span>
                    </>
                  )}
                </span>
              )}
            </>
          ) : (
            <>
              <span className="font-serif text-[19px] font-medium text-ink">Repos</span>
              <span className="rounded-full bg-active px-2.5 py-0.5 text-xs">read-only</span>
            </>
          )}
        </div>
        <div className="flex items-center gap-3.5 px-5 text-[13px] text-muted">
          {session.me.email && <span className="truncate">{session.me.email}</span>}
          <button type="button" onClick={() => void logout()} className="hover:text-ink">
            Sign out
          </button>
        </div>
      </header>

      <div className="grid min-h-0 grid-cols-[300px_1fr]">
        <aside className="flex min-h-0 flex-col border-r border-border bg-panel">
          <div className="px-6 pt-4 pb-1 text-[11px] font-medium uppercase tracking-[.12em] text-faint">Explore</div>
          <nav aria-label="Pages" className="grid gap-0.5 px-3 pb-1">
            {(
              [
                ["ask", "Ask", <AskIcon key="a" />],
                ["repos", "Repos", <ReposIcon key="r" />],
              ] as const
            ).map(([p, label, icon]) => (
              <button
                key={p}
                type="button"
                aria-current={page === p ? "page" : undefined}
                onClick={() => setPage(p)}
                className={navItem + " " + (page === p ? "bg-active text-ink" : "text-muted")}
              >
                {page === p && (
                  <span aria-hidden="true" className="absolute top-2 bottom-2 -left-3 w-[3px] rounded-r bg-accent" />
                )}
                {icon}
                {label}
              </button>
            ))}
          </nav>
          <div className="flex items-center px-6 pt-3 pb-1 text-[11px] font-medium uppercase tracking-[.12em] text-faint">
            History
            {threads.length > 0 && <span className="ml-auto font-mono tracking-normal">{threads.length}</span>}
          </div>
          <Threads
            activeId={threadId}
            onSelect={(id) => {
              setPage("ask");
              selectThread(id);
            }}
            version={threadsVersion}
            busy={busy}
            onList={setThreads}
          />
          {index && (
            <div className="m-3 flex items-center gap-2 rounded-ui border border-border bg-bg px-3.5 py-3 text-[13px] text-muted">
              <span
                aria-hidden="true"
                className={"h-[7px] w-[7px] rounded-full " + (index.ok ? "bg-online" : "bg-ochre")}
              />
              {index.ok ? "Index current" : "Index has errors"} · {index.when}
            </div>
          )}
        </aside>

        <main className="min-h-0 min-w-0">
          {/*
            Ask stays mounted and is hidden, never unmounted. Switching to Repos
            mid-answer would otherwise discard the thread on screen while the stream
            keeps writing into a dead component — and the stored record only catches
            up once the turn is finished.
          */}
          <div hidden={page !== "ask"} className="h-full">
            <Ask
              threadId={threadId}
              onThread={selectThread}
              onActivity={refreshThreads}
              onBusy={setBusy}
              onUsage={(u) =>
                // Compared by value: Ask reports on every change of its turn
                // list, which is once per streamed token, and a fresh object
                // each time would re-render the whole shell per token.
                setUsageTotal((prev) =>
                  prev && u && prev.tokens === u.tokens && prev.cost === u.cost ? prev : u,
                )
              }
            />
          </div>
          {page === "repos" && (
            <div className="h-full overflow-auto">
              <div className="max-w-[1100px] px-10 py-8">
                <h2 className="font-serif text-[28px] font-medium tracking-tight text-ink">Repositories</h2>
                <p className="mt-1 mb-6 text-[14.5px] text-muted">
                  Read-only. The repository list is maintained in <code className="font-mono">repos.yaml</code>,
                  and credentials never live in it. A repo that drops out of the file is deactivated, never
                  deleted.
                </p>
                <RepoList />
              </div>
            </div>
          )}
        </main>
      </div>
    </div>
  );
}
