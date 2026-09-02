import { useCallback, useEffect, useState } from "react";
import Ask from "./Ask";
import RepoList from "./RepoList";
import Threads from "./Threads";

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

export default function App() {
  const [page, setPage] = useState<Page>("ask");
  const [threadId, setThreadId] = useState<number | null>(storedThread);
  // Bumped whenever the list may have changed. The titles are written by the
  // server — a placeholder on Create, the model's version later from a
  // background goroutine — and neither can push.
  const [threadsVersion, setThreadsVersion] = useState(0);
  const [busy, setBusy] = useState(false);
  const session = useSession();

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
        <p className="text-sm text-[var(--color-ink-soft)]">
          {session.state === "checking" && "Checking the session …"}
          {session.state === "out" && "Redirecting to sign-in …"}
          {session.state === "halted" && session.message}
        </p>
        {session.state === "halted" && (
          <a
            href="/api/auth/login"
            className="mt-4 inline-block text-sm text-[var(--color-accent)]"
          >
            Sign in
          </a>
        )}
      </main>
    );
  }

  return (
    <main className="mx-auto max-w-6xl p-8">
      <header className="mb-8 flex items-baseline gap-6">
        <h1 className="text-2xl font-semibold tracking-tight">rongo</h1>
        <nav className="flex gap-4 text-sm">
          {(["ask", "repos"] as const).map((p) => (
            <button
              key={p}
              type="button"
              aria-current={page === p ? "page" : undefined}
              onClick={() => setPage(p)}
              className={
                page === p
                  ? "text-[var(--color-accent)]"
                  : "text-[var(--color-ink-soft)] hover:text-[var(--color-ink)]"
              }
            >
              {p === "ask" ? "Ask" : "Repos"}
            </button>
          ))}
        </nav>
        <button
          type="button"
          onClick={() => void logout()}
          className="ml-auto text-sm text-[var(--color-ink-soft)] hover:text-[var(--color-ink)]"
        >
          Sign out
        </button>
      </header>

      {/*
        Ask stays mounted and is hidden, never unmounted. Switching to Repos
        mid-answer would otherwise discard the thread on screen while the stream
        keeps writing into a dead component — and the stored record only catches
        up once the turn is finished.
      */}
      <div hidden={page !== "ask"} className="flex gap-8">
        <aside className="w-56 shrink-0">
          <Threads
            activeId={threadId}
            onSelect={selectThread}
            version={threadsVersion}
            busy={busy}
          />
        </aside>
        <div className="min-w-0 flex-1">
          <Ask
            threadId={threadId}
            onThread={selectThread}
            onActivity={refreshThreads}
            onBusy={setBusy}
          />
        </div>
      </div>
      {page === "repos" && (
        <>
          <p className="mb-4 text-sm text-[var(--color-ink-soft)]">
            Read-only. The repository list is maintained in <code>repos.yaml</code>,
            and credentials never live in it.
          </p>
          <RepoList />
        </>
      )}
    </main>
  );
}
