import { useState } from "react";

import { useLoaded, useReportCount } from "../hooks";
import { Icon } from "../Icon";
import { forgetMemory, listMemories, type Memory } from "./api";

/**
 * Every standing instruction this reader has given, in one place, newest
 * first. Rules are given in chat and only deleted here: a rule typed into a
 * form would skip the one step that makes it a rule, the understanding of a
 * question. English rows on an English page: memory is chrome, not an
 * answer, and one rule serves threads in four languages.
 */
function day(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}

export default function MemoryPage({
  onCount = () => {},
  onOpenThread,
}: {
  /** How many rules there are, for the header's pill; null until known. */
  onCount?: (n: number | null) => void;
  onOpenThread: (id: string) => void;
}) {
  const [state, setState] = useLoaded(async () => {
    const page = await listMemories();
    return { enabled: page.enabled, memories: page.memories };
  }, []);
  const [busy, setBusy] = useState<number | null>(null);

  useReportCount(state.state === "ready" && state.enabled ? state.memories.length : null, onCount);

  async function forget(m: Memory) {
    setBusy(m.id);
    try {
      if (!(await forgetMemory(m.id))) return;
      setState((prev) =>
        prev.state === "ready" ? { ...prev, memories: prev.memories.filter((x) => x.id !== m.id) } : prev,
      );
    } catch {
      // The row stays: saying a rule is gone when it is not is worse than
      // saying nothing.
    } finally {
      setBusy(null);
    }
  }

  if (state.state === "loading") return <p className="text-muted">Loading…</p>;
  if (state.state === "error")
    return (
      <p role="alert" className="text-accent-strong">
        The memory cannot be fetched.
      </p>
    );
  if (!state.enabled)
    return (
      <p className="text-muted">
        Memory is off for this deployment (<code className="font-mono">BACKEND_MEMORY=false</code>). Nothing said
        in chat is kept.
      </p>
    );
  if (state.memories.length === 0)
    return (
      <p className="text-muted">
        Nothing is kept yet. Tell rongo in chat: <span className="text-ink-dim">“never show me flowcharts”</span>,{" "}
        <span className="text-ink-dim">“from now on, skip the tests”</span>,{" "}
        <span className="text-ink-dim">“don't mention lerb-chooser-ui anymore”</span>.
      </p>
    );

  return (
    <ul className="m-0 list-none divide-y divide-border-soft rounded-ui border border-border bg-panel p-0">
      {state.memories.map((m) => (
        <li key={m.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-4 py-3">
          <span className="min-w-0 flex-1 text-[15px] text-ink">{m.text}</span>
          {/* Where the rule holds. A scope the index no longer carries is
              said as such, because the rule then holds everywhere rather than
              nowhere. */}
          {m.scope ? (
            <span
              className={
                "rounded-full px-2.5 py-0.5 text-xs " +
                (m.scope_live ? "bg-active text-muted" : "bg-active text-faint line-through")
              }
              title={m.scope_live ? `Applies to ${m.scope}` : `${m.scope} is no longer indexed; applies everywhere`}
            >
              {m.scope}
            </span>
          ) : (
            <span className="rounded-full bg-active px-2.5 py-0.5 text-xs text-muted">everywhere</span>
          )}
          <span className="font-mono text-[11.5px] text-faint">{day(m.created_at)}</span>
          {m.thread_id && (
            <button
              type="button"
              onClick={() => onOpenThread(m.thread_id!)}
              className="text-[13px] text-ink-dim underline-offset-[3px] hover:underline hover:decoration-accent"
            >
              from thread
            </button>
          )}
          <button
            type="button"
            aria-label={`Forget "${m.text}"`}
            disabled={busy === m.id}
            onClick={() => void forget(m)}
            className="inline-flex h-7 items-center gap-1.5 rounded-full border border-border bg-panel px-3 text-[13px] text-danger transition-colors hover:border-elevated-border hover:bg-active disabled:opacity-50"
          >
            <Icon name="eyeOff" size="14px" />
            Forget
          </button>
        </li>
      ))}
    </ul>
  );
}
