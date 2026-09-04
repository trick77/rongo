import { useCallback, useEffect, useState } from "react";
import Ask, { money } from "./Ask";
import { Icon } from "./Icon";
import RepoList, { lastRunAt, relative, type Repo } from "./RepoList";
import Threads, { railLabel, type Thread } from "./Threads";
import { PlusIcon } from "./icons";
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

/**
 * A rail button, ../loom's metrics: 26px tall, 14/20 text, 6px radius — its
 * SidebarItems.tsx has `h-[26px] rounded-md px-1.5 gap-2.5`. rounded-md rather
 * than the app's own rounded-ui-sm because the rail is loom's surface and 8px
 * reads rounder than loom's rows do; rounded-ui-sm stays for everything else.
 * The rail has two type sizes in total — this one and railLabel — plus the
 * mono timestamp on a thread row.
 */
const railRow =
  "flex h-[26px] pointer-coarse:h-11 w-full items-center gap-2.5 rounded-md px-1.5 text-left text-sm/5 " +
  "disabled:opacity-50";

export default function App() {
  const [page, setPage] = useState<Page>("ask");
  // Below lg the rail is an off-canvas drawer: 300px of it beside a 390px
  // phone left the thread 90px. There is no router, so this is the only place
  // the drawer's state can live.
  const [navOpen, setNavOpen] = useState(false);
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

  // Escape closes the drawer, the second way out beside the backdrop. Bound
  // unconditionally rather than only while open: a listener added and removed
  // on every toggle is more moving parts than one that reads the state.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setNavOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Nothing is rendered until the session is known: the alternative is a flash
  // of the signed-out app on every reload, and a redirect landing on top of it.
  if (session.state !== "in") {
    return (
      <main className="mx-auto max-w-6xl p-5 sm:p-8">
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
    // h-dvh, not h-screen: iOS Safari counts the collapsed toolbar strip into
    // 100vh, and the composer then sits under the toolbar with no way to reach
    // it. The short-viewport row is the landscape phone, where 56px of header
    // out of 390px is a tenth of the screen spent on chrome.
    <div className="grid h-dvh grid-rows-[56px_1fr] [@media(max-height:500px)]:grid-rows-[44px_1fr]">
      <header className="grid grid-cols-[auto_1fr_auto] items-center border-b border-border bg-panel lg:grid-cols-[300px_1fr_auto]">
        <div className="flex h-full items-center gap-2.5 px-2 lg:px-5">
          {/*
            Deliberately not disabled={busy}: the rail's rows are, but the way
            back TO the rail must not be. With the drawer shut and the toggle
            dead there would be no navigation at all while an answer streams.
          */}
          <button
            type="button"
            aria-label="Open navigation"
            aria-expanded={navOpen}
            aria-controls="nav-drawer"
            onClick={() => setNavOpen(true)}
            className="grid h-11 w-11 shrink-0 place-items-center rounded-ui-sm text-muted hover:bg-active hover:text-ink lg:hidden"
          >
            <Icon name="sidebar" size="21px" />
          </button>
          <span
            aria-hidden="true"
            className="h-[30px] w-[30px] shrink-0 rounded-lg bg-accent-fill bg-[length:auto_24px] bg-[3px_center] bg-no-repeat bg-blend-luminosity [@media(max-height:500px)]:h-[24px] [@media(max-height:500px)]:w-[24px]"
            style={{ backgroundImage: `url(${logo})` }}
          />
          {/*
            sr-only, never hidden: the wordmark is out of sight on a phone
            because the thread title needs the width, but the h1 is how a
            reader — and every test here — tells "signed in" from "not yet".
          */}
          <h1 className="sr-only font-serif text-[21px] font-semibold tracking-tight text-accent-strong sm:not-sr-only">
            rongo
          </h1>
        </div>
        {/* Baseline, not centre: the usage in small mono sits on the same
            line as the serif title, not floating beside its middle. */}
        <div className="flex min-w-0 items-baseline gap-2.5 px-2 text-muted lg:px-6">
          {page === "ask" ? (
            <>
              {/* No breadcrumb root: there is one list, and "Threads /" led
                  nowhere you could click. The title stands on its own, and
                  keeps the accent it was just given. */}
              <span className="truncate font-serif text-[19px] font-medium text-accent-strong">
                {openTitle ?? "New question"}
              </span>
              {total && (
                <span
                  aria-label="Thread usage"
                  // Out of sight below sm: the running total beside a serif
                  // title does not fit 360px, and the same figure sits in
                  // every turn's own usage block.
                  className="ml-2 hidden shrink-0 whitespace-nowrap font-mono text-xs text-faint sm:inline-block"
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
              <span className="font-serif text-[19px] font-medium text-accent-strong">Repos</span>
              <span className="rounded-full bg-active px-2.5 py-0.5 text-xs">read-only</span>
            </>
          )}
        </div>
        <div className="flex items-center gap-2 px-2 text-[13px] text-muted lg:gap-3.5 lg:px-5">
          {session.me.email && <span className="hidden truncate md:inline">{session.me.email}</span>}
          <button type="button" onClick={() => void logout()} className="hover:text-ink">
            Sign out
          </button>
        </div>
      </header>

      <div className="grid min-h-0 grid-cols-1 lg:grid-cols-[300px_1fr]">
        {/*
          The same rail at every width; below lg the box is off-canvas and
          slides in, ../loom's drawer. Its contents are untouched — one action
          on top, the history, Repos at the foot.

          Closed, it is `invisible`, not merely translated away: a rail parked
          off-screen still takes tab stops and still reads to a screen reader,
          so tabbing past the toggle on a phone walked invisibly through every
          thread row. `lg:visible` puts it back where the rail is the layout.
          The visibility is transitioned discretely so it still slides out
          rather than blinking away.
        */}
        <aside
          id="nav-drawer"
          className={
            "fixed inset-y-0 left-0 z-50 flex min-h-0 w-[300px] max-w-[85vw] flex-col border-r border-border bg-panel " +
            "transition-[transform,visibility] transition-discrete duration-200 ease-out " +
            "lg:visible lg:static lg:z-auto lg:w-auto lg:max-w-none lg:translate-x-0 " +
            (navOpen ? "visible translate-x-0" : "invisible -translate-x-full")
          }
        >
          {/*
            The one action, at the top where it belongs, and the way to Repos
            under it. New question used to sit inside Threads and therefore
            under the "History" heading, which read as if starting a question
            were a piece of history; Repos spent a release at the foot, where
            it read as part of the index status line rather than as a place.
          */}
          <div className="px-2 pt-2">
            <button
              type="button"
              onClick={() => {
                setPage("ask");
                selectThread(null);
                setNavOpen(false);
              }}
              disabled={busy}
              className={railRow + " text-rail hover:bg-rail-hover"}
            >
              <span
                aria-hidden="true"
                className="grid h-5 w-5 shrink-0 place-items-center rounded-full bg-elevated text-ink-dim"
              >
                <PlusIcon />
              </span>
              New question
            </button>
            <button
              type="button"
              aria-current={page === "repos" ? "page" : undefined}
              onClick={() => {
                setPage("repos");
                setNavOpen(false);
              }}
              className={railRow + " " + (page === "repos" ? "bg-rail-sel text-white" : "text-rail hover:bg-rail-hover")}
            >
              {/* The same 20px slot as the plus disc above. The Icon glyph is
                  text, so its box is whatever advance width the font gives it
                  — 21px here — and without the slot the two labels start a
                  pixel apart. */}
              <span className="grid h-5 w-5 shrink-0 place-items-center">
                <Icon name="code" size="21px" className="text-ink-dim" />
              </span>
              Repos
            </button>
          </div>
          {/* ../loom's label rhythm, taken from its SidebarSection: 20px above
              a label, 8px below it. The action block above therefore carries
              no bottom padding of its own — mt-5 alone is the whole gap, the
              way it is between loom's last primary item and its first section
              title.

              The 8px BELOW this label is spent by the first group in Threads,
              not here: on a morning with nothing asked yet the first thing
              under "History" is another label, and two stacked labels want the
              20px a label always gets, not 8. loom reaches the same 20 by
              margin collapsing through its empty section; rongo's scroller is
              a BFC, so the gap has to be owned on one side, and the group side
              is the one that knows which case it is.

              px-2 to sit in the same column as the day groups and the rows
              below, which are inside Threads' own px-2 scroller. */}
          <div className="mt-5 px-2">
            <div className={railLabel}>History</div>
          </div>
          <Threads
            activeId={threadId}
            onSelect={(id) => {
              setPage("ask");
              selectThread(id);
              setNavOpen(false);
            }}
            version={threadsVersion}
            busy={busy}
            onList={setThreads}
          />
          {/* The foot: the index line alone, and only when there is a repo
              list for it to speak about. */}
          {index && (
            <div className="p-2">
              <div className="flex items-center gap-2 rounded-ui border border-border bg-bg px-3.5 py-3 text-[13px] text-muted">
                <span
                  aria-hidden="true"
                  className={"h-[7px] w-[7px] rounded-full " + (index.ok ? "bg-online" : "bg-ochre")}
                />
                {index.ok ? "Index current" : "Index has errors"} · {index.when}
              </div>
            </div>
          )}
        </aside>
        {/*
          The way out, with no close button in the drawer: every row inside it
          closes it, and on a 360px phone the backdrop is still 60px of tap.
        */}
        {navOpen && (
          <div
            className="fixed inset-0 z-40 bg-black/50 lg:hidden"
            onClick={() => setNavOpen(false)}
            aria-hidden="true"
          />
        )}

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
              <div className="max-w-[1100px] px-4 py-6 sm:px-6 lg:px-10 lg:py-8">
                {/* leading-tight like Ask's welcome heading: without it the
                    taller line box puts this title 3px below the other page's. */}
                <h2 className="font-serif text-[22px] font-medium leading-tight tracking-tight text-ink sm:text-[28px]">
                  Repositories
                </h2>
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
