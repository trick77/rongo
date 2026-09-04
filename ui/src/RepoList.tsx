import { useEffect, useState } from "react";

/** One row of GET /api/repos. */
export type Repo = {
  name: string;
  branch: string;
  last_sha: string;
  last_run_at: string | null;
  files: number;
  chunks: number;
  modules: number;
  enabled: boolean;
  last_error: string;
};

/**
 * Three states, deliberately kept apart: still loading, could not be reached,
 * and reached but empty. Collapsing the last two would show "nothing is
 * indexed" to someone whose server simply could not answer.
 */
type State =
  | { kind: "loading" }
  | { kind: "failed"; message: string }
  | { kind: "loaded"; repos: Repo[] };

function shortSha(sha: string): string {
  return sha.slice(0, 7);
}

function ago(iso: string | null): string {
  if (!iso) return "never";
  const then = new Date(iso);
  if (Number.isNaN(then.getTime())) return "unknown";
  return then.toLocaleString("en-GB", { dateStyle: "short", timeStyle: "short" });
}

/** relative says how long ago a run happened, in the words a person uses. */
export function relative(iso: string | null, now = Date.now()): string {
  if (!iso) return "never";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "unknown";
  const min = Math.max(0, Math.round((now - then) / 60000));
  if (min < 1) return "just now";
  if (min < 60) return `${min} min ago`;
  const h = Math.round(min / 60);
  if (h < 24) return `${h} h ago`;
  return `${Math.round(h / 24)} d ago`;
}

/** lastRunAt is the most recent run across the list, or null when none ran. */
export function lastRunAt(repos: Repo[]): string | null {
  return (
    repos
      .map((r) => r.last_run_at)
      .filter((x): x is string => !!x)
      .sort()
      .at(-1) ?? null
  );
}

function rowState(r: Repo): string {
  if (r.last_error) return "error";
  if (!r.enabled) return "disabled";
  return "ok";
}

const th = "px-3.5 py-2.5 text-left text-[11px] font-medium uppercase tracking-[.12em] text-faint";

function Stat({ label, value, note }: { label: string; value: string | number; note?: string }) {
  return (
    // The bottom border is what separates the two-up rows on a phone; from sm
    // the block is one flex row again and only the vertical rules are left.
    <div className="flex-1 border-r border-b border-border px-4 py-3 last:border-r-0 sm:border-b-0 sm:px-5 sm:py-3.5">
      <div className="text-[11px] font-medium uppercase tracking-[.12em] text-faint">{label}</div>
      <div className="mt-0.5 font-serif text-[21px] leading-tight tabular-nums text-ink sm:text-[26px]">
        {value}
        {note && <small className="ml-1.5 font-sans text-[12.5px] text-muted">{note}</small>}
      </div>
    </div>
  );
}

export default function RepoList() {
  const [state, setState] = useState<State>({ kind: "loading" });

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/repos");
        if (!res.ok) {
          if (!cancelled) {
            setState({ kind: "failed", message: `Status ${res.status}` });
          }
          return;
        }
        const repos = (await res.json()) as Repo[];
        if (!cancelled) setState({ kind: "loaded", repos });
      } catch {
        if (!cancelled) setState({ kind: "failed", message: "network error" });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  if (state.kind === "loading") {
    return <p className="text-muted">Loading…</p>;
  }

  if (state.kind === "failed") {
    return (
      <p role="alert" className="text-accent-strong">
        The repository status cannot be fetched ({state.message}).
      </p>
    );
  }

  if (state.repos.length === 0) {
    return (
      <p className="text-muted">
        No repositories in <code className="font-mono">repos.yaml</code> yet.
      </p>
    );
  }

  const repos = state.repos;
  const active = repos.filter((r) => r.enabled).length;
  const sum = (pick: (r: Repo) => number) => repos.reduce((n, r) => n + pick(r), 0);
  const lastRun = lastRunAt(repos);

  return (
    <>
      {/* Five stats across a 360px phone is 70px each; two-up they still read
          as numbers. From sm it is the original single row. */}
      <div className="mb-5 grid grid-cols-2 overflow-hidden rounded-ui border border-border bg-panel sm:flex">
        <Stat label="Repositories" value={repos.length} note={`${active} active`} />
        <Stat label="Files" value={sum((r) => r.files)} />
        <Stat label="Chunks" value={sum((r) => r.chunks)} />
        <Stat label="Modules" value={sum((r) => r.modules)} />
        <Stat label="Last run" value={relative(lastRun)} />
      </div>
      {/*
        The frame moved out to a wrapper so seven columns scroll inside their
        own box. Without it the table overflowed the page's own scroller and
        dragged the whole Repos page sideways. The repository name stays put
        while the rest scrolls, so a row is always identifiable — and the
        error stripe and message, which live in that column, stay in sight.
      */}
      <div className="overflow-x-auto overscroll-x-contain rounded-ui border border-border">
        <table className="w-full min-w-[720px] border-separate border-spacing-0 bg-panel text-sm">
        <thead>
          <tr className="bg-bg">
            <th className={th + " sticky left-0 z-10 border-b border-border bg-bg"}>Repository</th>
            <th className={th + " border-b border-border"}>Branch</th>
            <th className={th + " border-b border-border"}>State</th>
            <th className={th + " border-b border-border"}>Last run</th>
            <th className={th + " border-b border-border text-right"}>Files</th>
            <th className={th + " border-b border-border text-right"}>Chunks</th>
            <th className={th + " border-b border-border text-right"}>Modules</th>
          </tr>
        </thead>
        <tbody>
          {repos.map((r) => {
            const st = rowState(r);
            return (
              <tr
                key={r.name}
                data-state={st}
                className={
                  "align-top [&>td]:border-b [&>td]:border-border-soft last:[&>td]:border-b-0 " +
                  (r.enabled ? "" : "text-faint")
                }
              >
                {/* The explicit background is what a sticky cell needs, or the
                    scrolled columns show through it. */}
                <td
                  className={
                    "sticky left-0 z-10 bg-panel px-3.5 py-3 " +
                    (r.last_error ? "shadow-[inset_3px_0_0_var(--color-danger)]" : "")
                  }
                >
                  <span className="font-mono font-medium">{r.name}</span>
                  {r.last_error && <div className="mt-1 text-[13px] text-accent-strong">{r.last_error}</div>}
                </td>
                <td className="whitespace-nowrap px-3.5 py-3 font-mono">{r.branch}</td>
                <td className="px-3.5 py-3">
                  {/* Disabled and error are independent facts: a deactivated
                      repo keeps its last error, and both are said. */}
                  <span className="flex flex-wrap gap-1">
                    {st === "ok" && (
                      <span className="rounded-full bg-accent-dim px-2.5 py-0.5 text-xs font-medium text-accent-strong">Indexed</span>
                    )}
                    {r.last_error && (
                      <span className="rounded-full bg-ochre-wash px-2.5 py-0.5 text-xs font-medium text-ochre">Error</span>
                    )}
                    {!r.enabled && (
                      <span className="rounded-full bg-active px-2.5 py-0.5 text-xs font-medium text-muted">Disabled</span>
                    )}
                  </span>
                </td>
                <td className="whitespace-nowrap px-3.5 py-3">
                  <code className="font-mono text-xs">{shortSha(r.last_sha)}</code>
                  <span className="ml-1.5 text-xs text-faint">{ago(r.last_run_at)}</span>
                </td>
                <td className="whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">{r.files}</td>
                <td className="whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">{r.chunks}</td>
                <td className="whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">{r.modules}</td>
              </tr>
            );
          })}
        </tbody>
        </table>
      </div>
    </>
  );
}
