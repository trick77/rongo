import { useCallback, useEffect, useState } from "react";
import Ask from "./Ask";
import ThreadUsageBadge from "./ThreadUsageBadge";
import type { ThreadTotal } from "./turns";
import { Icon } from "./Icon";
import { RailResizer } from "./RailResizer";
import RepoList, { lastRunAt, relative, type Repo } from "./RepoList";
import Threads, { type Thread } from "./Threads";
import ThreadMenu from "./ThreadMenu";
import ThreadsPage from "./ThreadsPage";
import { useMenuDismiss } from "./useMenuDismiss";
import { useThreadActions } from "./useThreadActions";
import { railRow } from "./rail";
import SharedLinks from "./share/SharedLinks";
import MemoryPage from "./memory/MemoryPage";
import { PlusIcon } from "./icons";
import { navigate, pathForRoute, routeFromLocation, type Route } from "./routing";
import { tabTitle } from "./tabTitle";

type Page = "ask" | "threads" | "projects" | "shared" | "memory";

type Me = { subject: string; email: string; is_admin: boolean; version: string };

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
  | { state: "login" }
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
  // "local" is password mode: there is no provider to sign out of.
  if (params.get("signed_out") === "local") {
    return "Signed out.";
  }
  if (params.has("signed_out")) {
    return "Signed out. The session at the provider is still live — sign out there as well to end it.";
  }
  if (params.has("auth_error")) {
    return "Sign-in could not be completed. Try again; if it keeps failing, the reason is in the server log.";
  }
  return null;
}

function isJson(res: Response): boolean {
  return (res.headers?.get("content-type") ?? "").includes("application/json");
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
      // Password mode: /api/auth/login sent the browser back here with the
      // marker, and the form is the whole sign-in. No /api/me first: it
      // would 401 and send the browser to the login route again.
      if (new URLSearchParams(window.location.search).get("login") === "password") {
        setSession({ state: "login" });
        return;
      }
      let res: Response;
      try {
        res = await fetch("/api/me");
      } catch {
        // A network error is not a signed-out session. Redirecting here would
        // bounce the user to the provider every time the connection drops, so
        // the app renders and its panels report their own failures.
        if (!cancelled)
          setSession({ state: "in", me: { subject: "", email: "", is_admin: false, version: "" } });
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
      if (res.status === 403 && !isJson(res)) {
        // An authenticating proxy in front of rongo whose cookie ran out:
        // it answers every request with its own HTML sign-in page and never
        // lets this one through. rongo itself never sends a 403 here. A
        // reload renders that page as a page, on this URL, so the proxy
        // brings the user back to the thread they were on.
        setSession({ state: "out" });
        window.location.reload();
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
 * The password-mode sign-in. A full navigation to "/" on success rather than
 * a state change: the gate reads the cookie through /api/me, and a reload is
 * the one path that runs it again from the top.
 */
function LoginForm() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await fetch("/api/auth/password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
      });
      if (res.ok) {
        window.location.href = "/";
        return;
      }
      // One message for both halves: the server does not say which was
      // wrong, and neither does the form.
      setError(
        res.status === 401
          ? "Wrong username or password."
          : `Sign-in failed (HTTP ${res.status}).`,
      );
    } catch {
      setError("Sign-in failed: the server could not be reached.");
    }
    setBusy(false);
  };

  const field =
    "mt-1 h-[38px] w-full rounded-ui-sm border border-border bg-bg px-3 text-ink outline-none focus:border-accent";
  return (
    <form onSubmit={submit} className="max-w-xs" aria-label="Sign in">
      <label className="block text-sm text-muted">
        Username
        <input
          name="username"
          autoComplete="username"
          autoFocus
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          className={field}
        />
      </label>
      <label className="mt-3 block text-sm text-muted">
        Password
        <input
          name="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className={field}
        />
      </label>
      {error && (
        <p role="alert" className="mt-3 text-sm text-danger">
          {error}
        </p>
      )}
      <button
        type="submit"
        disabled={busy || username === "" || password === ""}
        className="mt-4 rounded-full bg-accent-fill px-4 py-2 text-sm font-medium text-ink hover:bg-accent-strong disabled:opacity-50"
      >
        Sign in
      </button>
    </form>
  );
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
        // Only repos still being indexed count: one disabled in the YAML keeps
        // its last error in the record, but nobody is going to fix it here.
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

export default function App() {
  // The URL is where the app is. A thread has an address, so it can be sent to
  // someone, reloaded into, and reached with Back — none of which was true
  // while the open thread lived in localStorage.
  const [route, setRoute] = useState<Route>(routeFromLocation);
  // Below lg the rail is an off-canvas drawer: 300px of it beside a 390px
  // phone left the thread 90px. Not a route: it is not somewhere you are, and
  // Back must close a thread rather than a drawer.
  const [navOpen, setNavOpen] = useState(false);
  const page: Page =
    route.view === "threads" || route.view === "projects" || route.view === "shared" || route.view === "memory"
      ? route.view
      : "ask";
  const threadId = route.view === "thread" ? route.id : null;
  // Bumped whenever the list may have changed. The titles are written by the
  // server — a placeholder on Create, the model's version later from a
  // background goroutine — and neither can push.
  const [threadsVersion, setThreadsVersion] = useState(0);
  const [busy, setBusy] = useState(false);
  // The thread a running turn is being written into. Kept apart from threadId:
  // the two part company the moment the reader opens another thread while the
  // answer is still arriving, which they are free to do.
  const [busyThread, setBusyThread] = useState<string | null>(null);
  const [threads, setThreads] = useState<Thread[]>([]);
  // The open thread when the rail's page does not carry it: the rail is the
  // latest 30, and a thread opened by its address can be any age. Fetched on
  // its own only then, and keyed by the address so a rename (via
  // threadsVersion) refreshes it.
  const [summary, setSummary] = useState<Thread | null>(null);
  // How many live links there are, as the Shared page counts them. The page
  // is the one that lists them, so it is the one that knows.
  const [sharedCount, setSharedCount] = useState<number | null>(null);
  // How many standing instructions the reader has, as the Memory page counts
  // them, on the same terms.
  const [memoryCount, setMemoryCount] = useState<number | null>(null);
  // The open thread's running total, as Ask reports it: every turn on
  // screen summed. Shown in the header next to the title.
  const [usageTotal, setUsageTotal] = useState<ThreadTotal | null>(null);
  // Whether the header badge has opened the thread's stats. The pane itself
  // is Ask's, where the turns are; this is only the flag, because the badge
  // that opens it sits in a different cell of the shell's grid.
  const [threadStats, setThreadStats] = useState(false);
  const session = useSession();
  const index = useIndexStatus(session.state === "in", threadsVersion);

  // One way to move: push the URL, then render what it says. Nothing sets the
  // route without the address bar following, so Back and a click land on the
  // same state.
  const go = useCallback((next: Route, replace = false) => {
    navigate(next, replace);
    setRoute(next);
  }, []);

  const selectThread = useCallback(
    (id: string | null, replace = false) =>
      go(id === null ? { view: "new" } : { view: "thread", id }, replace),
    [go],
  );

  // A thread the reader did not choose to leave: it turned out to be deleted
  // or not theirs, or they just deleted it. The address is CORRECTED rather
  // than added to, or Back would return to the dead one, the correction would
  // fire again, and Back could never leave the app.
  const closeDeadThread = useCallback(() => selectThread(null, true), [selectThread]);

  // The address bar has to say what is on screen from the first paint. "/" is
  // the everyday case, but so is anything that does not name a route — "/",
  // "/nope", "/thread/1.5" all render the unasked question, and leaving one of
  // those in the bar means a reload lands somewhere the app never was.
  // replaceState, not push: nobody navigated here on purpose.
  //
  // Back and Forward then re-read the bar. pushState does not fire popstate,
  // so this is the only listener needed.
  useEffect(() => {
    const settled = pathForRoute(routeFromLocation());
    if (window.location.pathname !== settled) window.history.replaceState({}, "", settled);
    const onPop = () => setRoute(routeFromLocation());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const refreshThreads = useCallback(() => setThreadsVersion((v) => v + 1), []);

  // The header's own menu, ../loom's chevron beside the title: the same
  // actions the row offers, for a thread opened by its address whose row
  // may be nowhere on the rail. The rail hears of every change through
  // threadsVersion, and the summary read is keyed on it too.
  const [headerMenu, setHeaderMenu] = useState(false);
  useMenuDismiss(headerMenu, () => setHeaderMenu(false));
  const headerActions = useThreadActions({
    onRenamed: refreshThreads,
    onDeleted: () => {
      closeDeadThread();
      refreshThreads();
    },
    onShared: refreshThreads,
    onStarred: refreshThreads,
  });

  const inRail = threadId !== null && threads.some((t) => t.id === threadId);
  useEffect(() => {
    if (threadId === null || inRail) {
      setSummary(null);
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch(`/api/threads/${threadId}/summary`);
        if (!res.ok) return;
        const t: Thread = await res.json();
        if (!cancelled) setSummary(t);
      } catch {
        // The header says "New question" until the row is known, which is
        // what it says while the rail loads too.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [threadId, inRail, threadsVersion]);

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

  // Only a settled title reaches the header. Until the model's title call
  // lands, the row holds the question's first 48 runes, and putting that up
  // there showed a question cut mid-word — then cut a second time by the
  // header's own truncate. The rail keeps the placeholder, where first words
  // are what tells one pending row from another.
  const openThread =
    threadId === null
      ? null
      : (threads.find((t) => t.id === threadId) ?? (summary?.id === threadId ? summary : null));
  const openTitle = openThread && !openThread.title_pending ? openThread.title : null;

  // The tab follows the header: the same settled title, the same page name,
  // so a row of tabs can be told apart and a rename reaches the tab strip.
  // Above the session gate because it is a hook, so a sign-in screen already
  // carries the name of the page it will open on.
  useEffect(() => {
    document.title = tabTitle(route, openTitle);
  }, [route, openTitle]);

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
        {session.state === "login" ? (
          <LoginForm />
        ) : (
          <p className="text-sm text-muted">
            {session.state === "checking" && "Checking the session …"}
            {session.state === "out" && "Redirecting to sign-in …"}
            {session.state === "halted" && session.message}
          </p>
        )}
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

  const total = threadId === null ? null : usageTotal;

  return (
    // h-dvh, not h-screen: iOS Safari counts the collapsed toolbar strip into
    // 100vh, and the composer then sits under the toolbar with no way to reach
    // it. The short-viewport row is the landscape phone, where 56px of header
    // out of 390px is a tenth of the screen spent on chrome.
    <div className="grid h-dvh grid-rows-[56px_1fr] [@media(max-height:500px)]:grid-rows-[44px_1fr]">
      <header className="grid grid-cols-[auto_1fr_auto] items-center border-b border-border bg-panel lg:grid-cols-[var(--rail-w)_1fr_auto]">
        <div className="flex h-full items-center gap-2.5 px-2 lg:px-5">
          {/*
            Deliberately not disabled={busy}: with the drawer shut and the
            toggle dead there would be no navigation at all while an answer
            streams — and the rail behind it is fully live now, so the one
            thing standing between the reader and another thread would be
            this button.
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
          {/*
            sr-only, never hidden: the wordmark is out of sight on a phone
            because the thread title needs the width, but the h1 is how a
            reader — and every test here — tells "signed in" from "not yet".
          */}
          <h1 className="sr-only font-serif text-[21px] leading-7 font-medium text-wordmark sm:not-sr-only">
            Rongo
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
              {total && <ThreadUsageBadge total={total} onOpen={() => setThreadStats(true)} />}
              {/* Last in the row, after the usage: the title is what the
                  reader came for and the figure is what it cost, so the
                  handle on the record comes after both. Withheld, like the
                  row's kebab, while the answer is still being written into
                  this thread. self-center: the row aligns its baselines and
                  a glyph has none worth sitting on. */}
              {openThread && openTitle !== null && !(busy && busyThread === threadId) && (
                <span className="relative self-center">
                  <button
                    type="button"
                    aria-haspopup="menu"
                    aria-expanded={headerMenu}
                    aria-label={"Actions for " + openTitle}
                    onClick={() => setHeaderMenu((open) => !open)}
                    className="grid h-6 w-6 place-items-center rounded-md text-muted transition-colors hover:bg-active hover:text-ink"
                  >
                    {/* One glyph, turned towards what it opened: down onto
                        the menu below it. Never a swap. */}
                    <Icon
                      name="chevronRight"
                      size="16px"
                      className={"transition-transform " + (headerMenu ? "rotate-90" : "")}
                    />
                  </button>
                  {headerMenu && (
                    <ThreadMenu
                      className="right-0"
                      starred={openThread.starred}
                      onStar={() => {
                        setHeaderMenu(false);
                        headerActions.startStar(openThread);
                      }}
                      onShare={() => {
                        setHeaderMenu(false);
                        headerActions.startShare(openThread);
                      }}
                      onRename={() => {
                        setHeaderMenu(false);
                        headerActions.startRename(openThread);
                      }}
                      onDelete={() => {
                        setHeaderMenu(false);
                        headerActions.startDelete(openThread);
                      }}
                    />
                  )}
                </span>
              )}
            </>
          ) : page === "threads" ? (
            <span className="font-serif text-[19px] font-medium text-accent-strong">Threads</span>
          ) : page === "projects" ? (
            <>
              <span className="font-serif text-[19px] font-medium text-accent-strong">Projects</span>
              <span className="rounded-full bg-active px-2.5 py-0.5 text-xs">read-only</span>
            </>
          ) : page === "memory" ? (
            <>
              <span className="font-serif text-[19px] font-medium text-accent-strong">Memory</span>
              {memoryCount !== null && (
                <span className="rounded-full bg-active px-2.5 py-0.5 text-xs">
                  {memoryCount === 1 ? "1 rule" : `${memoryCount} rules`}
                </span>
              )}
            </>
          ) : (
            <>
              <span className="font-serif text-[19px] font-medium text-accent-strong">Shared</span>
              {sharedCount !== null && (
                <span className="rounded-full bg-active px-2.5 py-0.5 text-xs">
                  {sharedCount === 1 ? "1 live" : `${sharedCount} live`}
                </span>
              )}
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

      {/* --rail-w, ../loom's expanded rail by default and draggable from lg
          up. The header above runs the same column, so the seam under the
          wordmark stays on the rail's border at every width. position:relative
          is what RailResizer's absolute handle pins against. */}
      <div className="relative grid min-h-0 grid-cols-1 lg:grid-cols-[var(--rail-w)_1fr]">
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

          The rail scrolls as one: the action block, the history and the index
          line at the foot move together, so the list is never the only thing
          that gives when the window is short.
        */}
        <aside
          id="nav-drawer"
          className={
            "fixed inset-y-0 left-0 z-50 flex min-h-0 w-[300px] max-w-[85vw] flex-col overflow-y-auto border-r border-border bg-panel " +
            "transition-[transform,visibility] transition-discrete duration-200 ease-out " +
            "lg:visible lg:static lg:z-auto lg:w-auto lg:max-w-none lg:translate-x-0 " +
            (navOpen ? "visible translate-x-0" : "invisible -translate-x-full")
          }
        >
          {/*
            The one action, at the top where it belongs, and the way to Repos
            under it. New question used to sit inside Threads, among the past
            questions, which read as if starting one were already a piece of
            history; Repos spent a release at the foot, where it read as part
            of the index status line rather than as a place.
          */}
          <div className="px-2 pt-2">
            <button
              type="button"
              onClick={() => {
                selectThread(null);
                setNavOpen(false);
              }}
              // Live while an answer streams, like the rows under it. The
              // question cannot be SENT until the turn finishes, and the
              // composer says so in words; a dead button here would be the
              // one place left where the rail refuses without explaining.
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
            {/* Every thread, under the one action, as ../loom's Sidebar has
                it: the history below is the latest 30, and this is where the
                rest of it is. */}
            <button
              type="button"
              aria-current={page === "threads" ? "page" : undefined}
              onClick={() => {
                go({ view: "threads" });
                setNavOpen(false);
              }}
              className={railRow + " " + (page === "threads" ? "bg-rail-sel text-white" : "text-rail hover:bg-rail-hover")}
            >
              <span className="grid h-5 w-5 shrink-0 place-items-center">
                <Icon name="messages" size="21px" className="text-ink-dim" />
              </span>
              Threads
            </button>
            {/* The audit view for the links this reader has handed out. On
                the rail rather than in a settings modal Rongo does not have: a
                live link is a place, and it has to be somewhere you can go. */}
            <button
              type="button"
              aria-current={page === "shared" ? "page" : undefined}
              onClick={() => {
                go({ view: "shared" });
                setNavOpen(false);
              }}
              className={railRow + " " + (page === "shared" ? "bg-rail-sel text-white" : "text-rail hover:bg-rail-hover")}
            >
              {/* The same 20px slot as the plus disc above. The Icon glyph is
                  text, so its box is whatever advance width the font gives it
                  — 21px here — and without the slot the two labels start a
                  pixel apart. */}
              <span className="grid h-5 w-5 shrink-0 place-items-center">
                <Icon name="upload" size="21px" className="text-ink-dim" />
              </span>
              Shared
            </button>
            <button
              type="button"
              aria-current={page === "projects" ? "page" : undefined}
              onClick={() => {
                go({ view: "projects" });
                setNavOpen(false);
              }}
              className={railRow + " " + (page === "projects" ? "bg-rail-sel text-white" : "text-rail hover:bg-rail-hover")}
            >
              <span className="grid h-5 w-5 shrink-0 place-items-center">
                <Icon name="code" size="21px" className="text-ink-dim" />
              </span>
              Projects
            </button>
            {/* What rongo keeps in mind for this reader. A place, like Shared:
                a rule given in chat has to be somewhere you can go and take
                back. */}
            <button
              type="button"
              aria-current={page === "memory" ? "page" : undefined}
              onClick={() => {
                go({ view: "memory" });
                setNavOpen(false);
              }}
              className={railRow + " " + (page === "memory" ? "bg-rail-sel text-white" : "text-rail hover:bg-rail-hover")}
            >
              <span className="grid h-5 w-5 shrink-0 place-items-center">
                <Icon name="memory" size="21px" className="text-ink-dim" />
              </span>
              Memory
            </button>
          </div>
          {/* No heading over the list: everything below the two actions is
              history, and a label saying so named the obvious. The 20px that
              ../loom's SidebarSection puts above a section is still spent —
              by the first group in Threads, which is the side that knows
              whether it is painted. The action block above therefore carries
              no bottom padding of its own. */}
          <Threads
            activeId={threadId}
            onSelect={(id) => {
              selectThread(id);
              setNavOpen(false);
            }}
            version={threadsVersion}
            busy={busy}
            busyId={busyThread}
            onList={setThreads}
            onShared={refreshThreads}
            onStarred={refreshThreads}
            onDeleted={(id) => {
              // The thread on screen has just been deleted: close it, so the
              // view falls back to the empty ask page rather than holding a
              // conversation whose record is gone.
              if (id === threadId) closeDeadThread();
              refreshThreads();
            }}
            onRenamed={refreshThreads}
            onAllThreads={() => {
              go({ view: "threads" });
              setNavOpen(false);
            }}
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
        {/* The rail's right edge, draggable from lg up. Outside the aside so
            its overflow-y-auto cannot clip the handle, and after it in the DOM
            so the handle wins the overlap against the rail's border. */}
        <RailResizer />
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
              version={session.me.version}
              // Null from Ask is never a reader's choice: it is the thread
              // turning out to be deleted or not theirs. Correct the address
              // rather than push a second entry over the dead one.
              onThread={(id) => (id === null ? closeDeadThread() : selectThread(id))}
              onActivity={refreshThreads}
              onBusy={(b, id) => {
                setBusy(b);
                setBusyThread(b ? id : null);
              }}
              threadStatsOpen={threadStats}
              onCloseThreadStats={() => setThreadStats(false)}
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
          {page === "threads" && (
            <div className="h-full overflow-auto">
              <div className="mx-auto max-w-[900px] px-4 py-6 sm:px-6 lg:px-10 lg:py-8">
                <h2 className="mb-6 font-serif text-[22px] font-medium leading-tight tracking-tight text-ink sm:text-[28px]">
                  Threads
                </h2>
                <ThreadsPage
                  activeId={threadId}
                  version={threadsVersion}
                  onSelect={(id) => selectThread(id)}
                  onChanged={refreshThreads}
                  onDeleted={(id) => {
                    if (id === threadId) closeDeadThread();
                    refreshThreads();
                  }}
                />
              </div>
            </div>
          )}
          {page === "projects" && (
            <div className="h-full overflow-auto">
              {/* The same measure as Threads and Shared: a page that is
                  half again as wide as its neighbours reads as a different
                  tool. The table keeps its six columns inside it because
                  only the name column flexes; below that, it scrolls. */}
              <div className="mx-auto max-w-[900px] px-4 py-6 sm:px-6 lg:px-10 lg:py-8">
                {/* leading-tight like Ask's welcome heading: without it the
                    taller line box puts this title 3px below the other page's. */}
                <h2 className="font-serif text-[22px] font-medium leading-tight tracking-tight text-ink sm:text-[28px]">
                  Projects
                </h2>
                <p className="mt-1 mb-6 text-[14.5px] text-muted">
                  Read-only. A project is one product and the repositories it is built from; the list is
                  maintained in <code className="font-mono">repos.yaml</code>, and credentials never live
                  in it. A repo that drops out of the file is removed here too, index and checkout with it.
                </p>
                <RepoList />
              </div>
            </div>
          )}
          {page === "memory" && (
            <div className="h-full overflow-auto">
              <div className="mx-auto max-w-[900px] px-4 py-6 sm:px-6 lg:px-10 lg:py-8">
                <h2 className="font-serif text-[22px] font-medium leading-tight tracking-tight text-ink sm:text-[28px]">
                  Memory
                </h2>
                <p className="mt-1 mb-6 text-[14.5px] text-muted">
                  What rongo keeps in mind for every answer you get. Tell it in chat: “never …”, “from now
                  on …”, “don't mention … anymore”. A rule outranks the answer's own style rules and never
                  its sources, its language or your role. A rule stays until you delete it here.
                </p>
                <MemoryPage onCount={setMemoryCount} onOpenThread={(id) => selectThread(id)} />
              </div>
            </div>
          )}
          {page === "shared" && (
            <div className="h-full overflow-auto">
              <div className="mx-auto max-w-[900px] px-4 py-6 sm:px-6 lg:px-10 lg:py-8">
                <h2 className="font-serif text-[22px] font-medium leading-tight tracking-tight text-ink sm:text-[28px]">
                  Shared threads
                </h2>
                <p className="mt-1 mb-6 text-[14.5px] text-muted">
                  Every link below is readable by anyone who has it, without signing in. A link is frozen at
                  the turn it was made: later questions are not on it until you update it. Revoking is
                  immediate, and sharing again returns the same link.
                </p>
                <SharedLinks
                  onCount={setSharedCount}
                  onChange={() => {
                    refreshThreads();
                  }}
                  onOpenThread={(id) => selectThread(id)}
                />
              </div>
            </div>
          )}
        </main>
      </div>
      {/* The header menu's dialogs. The rail and the Threads page carry
          their own. */}
      {headerActions.dialogs}
    </div>
  );
}
