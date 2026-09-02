import { useEffect, useState } from "react";

export type Thread = {
  id: number;
  title: string;
  created_at: string;
};

/**
 * The thread list. Titles only — the conversation itself is a record, and this
 * is the way back into it after a reload.
 *
 * The list is reloaded whenever `version` changes rather than on a timer. Two
 * moments need it: the placeholder title appears the instant a question is
 * sent, and the model-written title replaces it later from a background
 * goroutine that has no way to push.
 */
export default function Threads({
  activeId,
  onSelect,
  version,
  busy = false,
}: {
  activeId: number | null;
  onSelect: (id: number | null) => void;
  version: number;
  busy?: boolean;
}) {
  const [threads, setThreads] = useState<Thread[]>([]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/threads");
        if (!res.ok) return;
        const list = await res.json();
        if (!cancelled && Array.isArray(list)) setThreads(list);
      } catch {
        // A list that cannot be loaded is not worth an error banner: asking a
        // new question still works, and that is the important path.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [version]);

  return (
    <nav aria-label="Threads" className="text-sm">
      <button
        type="button"
        onClick={() => onSelect(null)}
        disabled={busy}
        className="mb-3 w-full rounded border border-[var(--color-hairline)] px-3 py-1 text-left text-[var(--color-ink-soft)] hover:text-[var(--color-ink)] disabled:opacity-50"
      >
        New question
      </button>
      <ul className="space-y-1">
        {threads.map((t) => (
          <li key={t.id}>
            <button
              type="button"
              aria-current={t.id === activeId ? "true" : undefined}
              onClick={() => onSelect(t.id)}
              disabled={busy}
              className={
                "w-full truncate rounded px-3 py-1 text-left disabled:opacity-50 " +
                (t.id === activeId
                  ? "bg-[var(--color-accent-wash)] text-[var(--color-accent)]"
                  : "text-[var(--color-ink-soft)] hover:text-[var(--color-ink)]")
              }
            >
              {t.title}
            </button>
          </li>
        ))}
      </ul>
    </nav>
  );
}
