import { useEffect, useState } from "react";

/**
 * The rail's label size, ../loom's: 12/16 in sentence case, not an uppercase
 * tracked eyebrow. Exported because the "History" umbrella in App is the same
 * label as the day groups below it and must not drift from them.
 */
export const railLabel = "pl-1.5 text-xs/4 text-muted";

export type Thread = {
  id: number;
  title: string;
  created_at: string;
};

/** The day group a thread lands in, in the words the rail uses. */
function group(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "Earlier";
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(now) - day(d)) / 86400000);
  if (diff <= 0) return "Today";
  if (diff === 1) return "Yesterday";
  if (diff < 7) return "This week";
  return d.toLocaleString("en-GB", { month: "long", year: d.getFullYear() === now.getFullYear() ? undefined : "numeric" });
}

/** The short time shown next to a title: the clock today, the date otherwise. */
function when(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const sameDay = d.toDateString() === now.toDateString();
  return sameDay
    ? d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleDateString("en-GB", { day: "numeric", month: "short" });
}

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
  onList = () => {},
}: {
  activeId: number | null;
  /** Only ever a real thread: clearing to a new question is the rail's job. */
  onSelect: (id: number) => void;
  version: number;
  busy?: boolean;
  /** Reports the loaded list, so the shell can name the open thread. */
  onList?: (list: Thread[]) => void;
}) {
  const [threads, setThreads] = useState<Thread[]>([]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/threads");
        if (!res.ok) return;
        const list = await res.json();
        if (!cancelled && Array.isArray(list)) {
          setThreads(list);
          onList(list);
        }
      } catch {
        // A list that cannot be loaded is not worth an error banner: asking a
        // new question still works, and that is the important path.
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version]);

  const groups: { label: string; items: Thread[] }[] = [];
  for (const t of threads) {
    const label = group(t.created_at);
    const last = groups[groups.length - 1];
    if (last && last.label === label) last.items.push(t);
    else groups.push({ label, items: [t] });
  }

  const item =
    "flex h-7 w-full items-center gap-2 rounded-ui-sm pr-1 pl-1.5 text-left text-sm/5 disabled:opacity-50 " +
    "hover:bg-active hover:text-ink";

  return (
    <nav aria-label="Threads" className="flex min-h-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 overflow-auto px-2 pb-3">
        {groups.map((g) => (
          <div key={g.label}>
            <h3 className={"mt-3 mb-1 " + railLabel}>{g.label}</h3>
            {/* No gap: the 28px row pitch is the rhythm, as ../loom has it. */}
            <ul className="flex flex-col">
              {g.items.map((t) => {
                const active = t.id === activeId;
                return (
                  <li key={t.id} className="min-w-0">
                    <button
                      type="button"
                      aria-current={active ? "true" : undefined}
                      onClick={() => onSelect(t.id)}
                      // Switching away from a running turn is what busy locks
                      // out. The running thread's own row is not a switch: it
                      // is the way back from the Repos page while the answer
                      // is still being written, and with the page nav gone it
                      // is the only one.
                      disabled={busy && !active}
                      className={item + " group " + (active ? "bg-active text-ink" : "text-muted")}
                    >
                      {/* The title runs out under a gradient to the row's own
                          background rather than ending in an ellipsis, as
                          ../loom's sidebar does. The text stays whole. */}
                      <span className="relative min-w-0 flex-1 overflow-hidden whitespace-nowrap">
                        {t.title}
                        <span
                          aria-hidden="true"
                          className={
                            "pointer-events-none absolute inset-y-0 right-0 w-9 bg-gradient-to-r from-transparent group-hover:to-active " +
                            (active ? "to-active" : "to-panel")
                          }
                        />
                      </span>
                      {active && busy && (
                        <span aria-hidden="true" className="pulse h-1.5 w-1.5 shrink-0 self-center rounded-full bg-accent-strong" />
                      )}
                      <time aria-hidden="true" className="shrink-0 font-mono text-[11px] text-faint">
                        {when(t.created_at)}
                      </time>
                    </button>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </div>
    </nav>
  );
}
