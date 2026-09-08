import { useEffect, useState } from "react";
import { DiagramSvg, type FlowSpec } from "./diagram";

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
  /** The product this repository belongs to. A repository standing alone is a
   * project of one named after itself, so this is never empty in practice. */
  project: string;
  /** What part it plays, free-form: backend, ui, consumer, contract. */
  kind: string;
  /** One sentence saying what it does. */
  description: string;
  /** The siblings it depends on, inside the same project. */
  uses: string[];
};

/** Project is one product and the repositories it is made of. */
export type Project = { name: string; repos: Repo[] };

/** byProject groups the flat list the API returns, projects sorted by name and
 * members sorted within each. The API already sorts by repository name; the
 * grouping must not lean on that, because the page's whole claim is that a
 * project is one thing however its members happen to arrive. */
export function byProject(repos: Repo[]): Project[] {
  const at = new Map<string, Repo[]>();
  for (const r of repos) {
    const key = r.project || r.name;
    const list = at.get(key);
    if (list) list.push(r);
    else at.set(key, [r]);
  }
  return [...at.entries()]
    .map(([name, rs]) => ({ name, repos: [...rs].sort((a, b) => a.name.localeCompare(b.name)) }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

/** wiringSpec turns a project's declared `uses` edges into the flow spec
 * rongo's own diagram renderer already draws.
 *
 * No new renderer and no library: layoutFlow removes back edges by DFS, ranks
 * by longest path and orders each rank by parent barycentre, so a cascade, a
 * diamond and a cycle all come out right and a shared repository is drawn once.
 * mermaid is ruled out here on purpose (see diagram.tsx) — and this spec is
 * built by rongo from repos.yaml rather than written by a model, so that file's
 * injection reasoning holds all the more.
 *
 * Every node carries src: [] because the graph is CONFIGURATION, not code.
 * AGENTS.md says a node cites code or nothing and is still drawn with no
 * sources, which is exactly this case: there is nothing to cite.
 *
 * An entry point — a repository nothing in the project uses — comes out as a
 * pill, which is what the flow renderer already does for `start`.
 *
 * Returns null when there is no edge to draw. A project of one has no wiring,
 * and neither has a set of repositories nobody declared a connection between:
 * a picture of unconnected boxes says less than the table under it. */
export function wiringSpec(p: Project): FlowSpec | null {
  const member = new Set(p.repos.map((r) => r.name));
  const edges = p.repos.flatMap((r) =>
    (r.uses ?? []).filter((u) => member.has(u)).map((u) => ({ from: r.name, to: u })),
  );
  if (edges.length === 0) return null;
  const reached = new Set(edges.map((e) => e.to));
  return {
    type: "flow",
    nodes: p.repos.map((r) => ({
      id: r.name,
      label: r.kind ? `${r.name} · ${r.kind}` : r.name,
      kind: reached.has(r.name) ? "step" : "start",
      src: [],
    })),
    edges,
  };
}

/** unconnected are the members no declared edge touches in either direction.
 * They are listed under the picture rather than drawn into it: nothing in the
 * project reaches them, and that is a fact worth stating — it is what keeps an
 * answer about the storefront from attributing a call to the admin API. */
export function unconnected(p: Project): Repo[] {
  const member = new Set(p.repos.map((r) => r.name));
  const touched = new Set<string>();
  for (const r of p.repos) {
    for (const u of r.uses ?? []) {
      if (member.has(u)) {
        touched.add(r.name);
        touched.add(u);
      }
    }
  }
  return touched.size === 0 ? [] : p.repos.filter((r) => !touched.has(r.name));
}

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
    // The bottom border separates the two-up rows on a phone; from sm the
    // block is one flex row again and only the vertical rules are left. The
    // even: and last: rules keep a cell from drawing its own rule flush
    // against the wrapper's border, which reads as a doubled line — and the
    // odd fifth stat takes the whole last row rather than leaving half of it
    // ruled and half of it blank.
    <div className="flex-1 border-r border-b border-border px-4 py-3 even:border-r-0 last:col-span-2 last:border-r-0 last:border-b-0 sm:border-b-0 sm:px-5 sm:py-3.5 sm:even:border-r sm:last:border-r-0">
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
  const projects = byProject(repos);
  const active = repos.filter((r) => r.enabled).length;
  const sum = (pick: (r: Repo) => number) => repos.reduce((n, r) => n + pick(r), 0);
  const lastRun = lastRunAt(repos);

  return (
    <>
      {/* Six stats across a 360px phone is 60px each; two-up they still read as
          numbers. From sm it is one row again. Projects leads Repositories: the
          project is what a reader is asked to choose between, and the
          repository count is how that product is built. */}
      <div className="mb-5 grid grid-cols-2 overflow-hidden rounded-ui border border-border bg-panel sm:flex">
        <Stat label="Projects" value={projects.length} />
        <Stat label="Repositories" value={repos.length} note={`${active} active`} />
        <Stat label="Files" value={sum((r) => r.files)} />
        <Stat label="Chunks" value={sum((r) => r.chunks)} />
        <Stat label="Modules" value={sum((r) => r.modules)} />
        <Stat label="Last run" value={relative(lastRun)} />
      </div>
      {projects.map((p) => (
        <ProjectPanel key={p.name} project={p} />
      ))}
    </>
  );
}

/** ProjectPanel is one product: what it is made of, how its repositories are
 * wired together, and the index status of each.
 *
 * A project of one gets a header and a single row and no wiring, because there
 * is none. That is rongo's own shape and almost every setup's, so it is the
 * case that has to stay quiet. */
function ProjectPanel({ project }: { project: Project }) {
  const spec = wiringSpec(project);
  const loose = unconnected(project);
  const sum = (pick: (r: Repo) => number) => project.repos.reduce((n, r) => n + pick(r), 0);

  return (
    <section className="mb-5 overflow-hidden rounded-ui border border-border bg-panel">
      <header className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1 border-b border-border bg-bg px-3.5 py-2.5">
        <h3 className="font-medium text-ink">{project.name}</h3>
        <span className="text-[12.5px] text-faint">
          {project.repos.length} {project.repos.length === 1 ? "repository" : "repositories"} ·{" "}
          {sum((r) => r.files)} files · {sum((r) => r.chunks)} chunks
        </span>
      </header>

      {spec && (
        <div className="border-b border-border px-3.5 pt-3.5 pb-2">
          {/* No hooks: every node carries src: [], so no chip is ever drawn.
              The graph is configuration, and configuration is not citable. */}
          <div className="overflow-x-auto overscroll-x-contain">
            <DiagramSvg spec={spec} hooks={{}} />
          </div>
          <p className="mt-1.5 text-[12px] text-faint">
            Arrows are <code className="font-mono">uses:</code> entries from{" "}
            <code className="font-mono">repos.yaml</code>. A repository with no arrow is reached from
            outside the project — a browser, a queue, another product.
          </p>
          {loose.length > 0 && (
            <p className="mt-1 text-[12px] text-faint">
              No declared connection:{" "}
              <span className="font-mono text-muted">{loose.map((r) => r.name).join(", ")}</span>
            </p>
          )}
        </div>
      )}

      <div className="overflow-x-auto overscroll-x-contain">
        <table className="w-full min-w-[760px] border-separate border-spacing-0 text-sm">
          <thead>
            <tr className="bg-bg">
              <th className={th + " sticky left-0 z-10 border-b border-border bg-bg"}>Repository</th>
              <th className={th + " border-b border-border"}>Part</th>
              <th className={th + " border-b border-border"}>Branch</th>
              <th className={th + " border-b border-border"}>State</th>
              <th className={th + " border-b border-border"}>Last run</th>
              <th className={th + " border-b border-border text-right"}>Files</th>
              <th className={th + " border-b border-border text-right"}>Chunks</th>
              <th className={th + " border-b border-border text-right"}>Modules</th>
            </tr>
          </thead>
          <tbody>
            {project.repos.map((r) => {
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
                    {r.description && (
                      <div className="mt-0.5 max-w-[28rem] text-[12.5px] text-muted">
                        {r.description}
                      </div>
                    )}
                    {r.last_error && (
                      <div className="mt-1 text-[13px] text-accent-strong">{r.last_error}</div>
                    )}
                  </td>
                  <td className="px-3.5 py-3">
                    {r.kind ? (
                      <span className="rounded-full bg-active px-2.5 py-0.5 font-mono text-[11px] text-ink-dim">
                        {r.kind}
                      </span>
                    ) : (
                      <span className="text-faint">—</span>
                    )}
                  </td>
                  <td className="whitespace-nowrap px-3.5 py-3 font-mono">{r.branch}</td>
                  <td className="px-3.5 py-3">
                    {/* Disabled and error are independent facts: a repo the YAML
                        disabled keeps its last error, and both are said. */}
                    <span className="flex flex-wrap gap-1">
                      {st === "ok" && (
                        <span className="rounded-full bg-accent-dim px-2.5 py-0.5 text-xs font-medium text-accent-strong">
                          Indexed
                        </span>
                      )}
                      {r.last_error && (
                        <span className="rounded-full bg-ochre-wash px-2.5 py-0.5 text-xs font-medium text-ochre">
                          Error
                        </span>
                      )}
                      {!r.enabled && (
                        <span className="rounded-full bg-active px-2.5 py-0.5 text-xs font-medium text-muted">
                          Disabled
                        </span>
                      )}
                    </span>
                  </td>
                  <td className="whitespace-nowrap px-3.5 py-3">
                    <code className="font-mono text-xs">{shortSha(r.last_sha)}</code>
                    <span className="ml-1.5 text-xs text-faint">{ago(r.last_run_at)}</span>
                  </td>
                  <td className="whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">
                    {r.files}
                  </td>
                  <td className="whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">
                    {r.chunks}
                  </td>
                  <td className="whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">
                    {r.modules}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

