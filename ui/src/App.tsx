import { useCallback, useState } from "react";
import Ask from "./Ask";
import RepoList from "./RepoList";
import Threads from "./Threads";

type Page = "fragen" | "repos";

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

  const selectThread = useCallback((id: number | null) => {
    setThreadId(id);
    rememberThread(id);
  }, []);

  const refreshThreads = useCallback(() => setThreadsVersion((v) => v + 1), []);

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
