import { useState } from "react";
import Ask from "./Ask";
import RepoList from "./RepoList";

type Page = "fragen" | "repos";

export default function App() {
  const [page, setPage] = useState<Page>("fragen");

  return (
    <main className="mx-auto max-w-4xl p-8">
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

      {page === "fragen" ? (
        <Ask />
      ) : (
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
