import { useCallback, useEffect, useState } from "react";
import Ask from "./Ask";
import RepoList from "./RepoList";
import Threads from "./Threads";

type Page = "fragen" | "repos";

type Me = { subject: string; email: string; is_admin: boolean };

/**
 * The session gate. /api/me is the one request the app makes before it renders
 * anything, so an expired cookie sends the browser to the provider instead of
 * letting every panel fail with its own 401.
 *
 * "checking" is a real state, not a detail: rendering the app first and
 * redirecting afterwards shows a flash of an empty, signed-out UI on every
 * reload.
 */
type Session =
  | { state: "checking" }
  | { state: "in"; me: Me }
  | { state: "out" };

function useSession(): Session {
  const [session, setSession] = useState<Session>({ state: "checking" });

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const res = await fetch("/api/me");
        if (cancelled) return;
        if (res.status === 401) {
          setSession({ state: "out" });
          // A full navigation, not a fetch: the provider answers with a login
          // page and a redirect chain, neither of which a fetch can follow
          // into the address bar.
          window.location.href = "/api/auth/login";
          return;
        }
        if (!res.ok) throw new Error(String(res.status));
        setSession({ state: "in", me: (await res.json()) as Me });
      } catch {
        // A network error is not a signed-out session. Redirecting here would
        // bounce the user to the provider every time the backend hiccups, so
        // the app renders and its panels report their own failures.
        if (!cancelled) setSession({ state: "in", me: { subject: "", email: "", is_admin: false } });
      }
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
    window.location.href = body.redirect_url ?? "/";
  } catch {
    // The cookie may or may not be gone; reloading lets the session gate above
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
  const [page, setPage] = useState<Page>("fragen");
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
        <p className="text-sm text-[var(--color-ink-soft)]">
          {session.state === "checking" ? "Anmeldung wird geprüft …" : "Weiterleitung zur Anmeldung …"}
        </p>
      </main>
    );
  }

  return (
    <main className="mx-auto max-w-6xl p-8">
      <header className="mb-8 flex items-baseline gap-6">
        <h1 className="text-2xl font-semibold tracking-tight">rongo</h1>
        <nav className="flex gap-4 text-sm">
          {(["fragen", "repos"] as const).map((p) => (
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
              {p === "fragen" ? "Fragen" : "Repos"}
            </button>
          ))}
        </nav>
        <button
          type="button"
          onClick={() => void logout()}
          className="ml-auto text-sm text-[var(--color-ink-soft)] hover:text-[var(--color-ink)]"
        >
          Abmelden
        </button>
      </header>

      {/*
        Ask stays mounted and is hidden, never unmounted. Switching to Repos
        mid-answer would otherwise discard the thread on screen while the stream
        keeps writing into a dead component — and the stored record only catches
        up once the turn is finished.
      */}
      <div hidden={page !== "fragen"} className="flex gap-8">
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
            Reine Anzeige. Die Repository-Liste wird in <code>repos.yaml</code> gepflegt,
            Zugangsdaten stehen nie darin.
          </p>
          <RepoList />
        </>
      )}
    </main>
  );
}
