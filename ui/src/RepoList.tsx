import { useEffect, useState } from "react";

/** One row of GET /api/repos. */
type Repo = {
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

function rowState(r: Repo): string {
  if (r.last_error) return "error";
  if (!r.enabled) return "disabled";
  return "ok";
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
    return <p className="text-[var(--color-ink-soft)]">Loading…</p>;
  }

  if (state.kind === "failed") {
    return (
      <p role="alert" className="text-[var(--color-ochre)]">
        The repository status cannot be fetched ({state.message}).
      </p>
    );
  }

  if (state.repos.length === 0) {
    return (
      <p className="text-[var(--color-ink-soft)]">
        No repositories in <code>repos.yaml</code> yet.
      </p>
    );
  }

  return (
    <table className="w-full border-collapse text-sm">
      <thead>
        <tr className="border-b border-[var(--color-hairline)] text-left text-[var(--color-ink-faint)]">
          <th className="py-2 font-medium">Repository</th>
          <th className="py-2 font-medium">Branch</th>
          <th className="py-2 font-medium">State</th>
          <th className="py-2 text-right font-medium">Files</th>
          <th className="py-2 text-right font-medium">Chunks</th>
          <th className="py-2 text-right font-medium">Modules</th>
        </tr>
      </thead>
      <tbody>
        {state.repos.map((r) => (
          <tr
            key={r.name}
            data-state={rowState(r)}
            className="border-b border-[var(--color-hairline)] align-top"
          >
            <td className="py-2">
              <span className="font-medium">{r.name}</span>
              {!r.enabled && (
                <span className="ml-2 text-xs text-[var(--color-ink-faint)]">Disabled</span>
              )}
              {r.last_error && (
                <div className="mt-1 text-xs text-[var(--color-ochre)]">{r.last_error}</div>
              )}
            </td>
            <td className="py-2">{r.branch}</td>
            <td className="py-2 whitespace-nowrap">
              <code>{shortSha(r.last_sha)}</code>
              <span className="ml-2 text-xs text-[var(--color-ink-faint)]">{ago(r.last_run_at)}</span>
            </td>
            <td className="py-2 text-right tabular-nums">{r.files}</td>
            <td className="py-2 text-right tabular-nums">{r.chunks}</td>
            <td className="py-2 text-right tabular-nums">{r.modules}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
