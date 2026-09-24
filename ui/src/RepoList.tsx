import { Fragment, useEffect, useState } from "react";
import { shortSha } from "./turns";
import { MermaidSvg, useDrawn, type FlowNode, type FlowSpec } from "./diagram";
import { toMermaid } from "./diagramExport";

/** One row of GET /api/repos. */
export type Repo = {
  name: string;
  branch: string;
  last_sha: string;
  /** The last poll, whatever it found: the poller's pulse, not the index. */
  last_run_at: string | null;
  /** The last time the index was written; null until the first run. This is
   * what "is my push in the answers yet" reads. */
  last_indexed_at: string | null;
  files: number;
  chunks: number;
  modules: number;
  enabled: boolean;
  /** A hand-extracted source drop rather than a clone: no remote, so its commit
   * never moves on its own. Drawn, because without the word a correct one-off
   * index reads exactly like a poller that stopped. */
  snapshot: boolean;
  last_error: string;
  /** The product this repository belongs to. A repository standing alone is a
   * project of one named after itself, so this is never empty in practice. */
  project: string;
  /** What part it plays, free-form: backend, ui, consumer, contract. Named for
   * this table's own column header — repos.yaml calls the field `part` too. */
  part: string;
  /** One sentence saying what it does. */
  description: string;
  /** The container image it is built into, tag-less, as repos.yaml declares
   * it: what a release turn pairs a deployed version with. Optional because a
   * row from an older build has no such field. */
  image?: string;
  /** The siblings it depends on, inside the same project, and any library. */
  uses: string[];
  /** A shared library from the `libraries:` block: a project of one no product
   * owns, which any project's `uses` may name. Optional because a row from an
   * older build has no such field. */
  library?: boolean;
  /** The commit lane: how many commits are recorded for the branch and when
   * the newest was made. Optional because a row from an older build has
   * neither; a snapshot has none by design. */
  commits?: number;
  newest_commit_at?: string | null;
};

/** historyLine says what the commit lane holds for one repository, for the
 * tooltip beside its indexed time. A snapshot has no history and says so. */
export function historyLine(r: Repo): string {
  if (r.snapshot) return "No history: a snapshot has no commits.";
  const n = r.commits ?? 0;
  if (n === 0) return "No commits recorded yet.";
  return `${n} ${n === 1 ? "commit" : "commits"} recorded, newest ${ago(r.newest_commit_at ?? null)}.`;
}

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

/** libraryNames is the set of shared libraries on the page, which is what a
 * project's wiring needs to draw an edge leaving it: the target is not a
 * member, and it is the one non-member an edge may legitimately point at. */
export function libraryNames(repos: Repo[]): Set<string> {
  return new Set(repos.filter((r) => r.library).map((r) => r.name));
}

/** usedBy lists the repositories naming a library in their `uses`, sorted, for
 * the line under the library's own heading. It is the one fact the library
 * panel has that its table does not: which products are built on it. */
export function usedBy(library: string, repos: Repo[]): string[] {
  return repos
    .filter((r) => (r.uses ?? []).includes(library))
    .map((r) => r.name)
    .sort((a, b) => a.localeCompare(b));
}

/** wiringSpec turns a project's declared `uses` edges into the flow spec
 * an answer's diagram used to be written in, which toMermaid then writes as
 * the source the renderer draws. The spec is kept as the middle step because
 * it already says what a node is, and a test can assert on it without
 * parsing diagram syntax.
 *
 * Every node carries src: [] because the graph is CONFIGURATION, not code:
 * there is nothing to cite.
 *
 * An entry point — a repository nothing in the project uses — comes out as a
 * pill, which is what `start` draws as.
 *
 * An edge to a library leaves the project, and is drawn as leaving it: the
 * library is a node of its own at the edge of the picture, an `end` so it
 * reads as a terminal rather than a member, labelled as a library. Any other
 * target outside the project is ignored — repos.Load refuses one, so it can
 * only be a hand-written row, and an arrow into nowhere is worse than none.
 *
 * Returns null when there is no edge to draw. A project of one has no wiring,
 * and neither has a set of repositories nobody declared a connection between:
 * a picture of unconnected boxes says less than the table under it. */
export function wiringSpec(p: Project, libraries: Set<string> = new Set()): FlowSpec | null {
  const member = new Set(p.repos.map((r) => r.name));
  const edges = p.repos.flatMap((r) =>
    (r.uses ?? [])
      .filter((u) => member.has(u) || libraries.has(u))
      .map((u) => ({ from: r.name, to: u })),
  );
  if (edges.length === 0) return null;
  const reached = new Set(edges.map((e) => e.to));
  // Only the repositories an edge actually touches. A member nothing connects
  // to would otherwise be drawn as a box floating beside the graph AND named
  // in the line beneath it — the same fact twice, once as a picture that says
  // nothing. It belongs in the list alone.
  const touched = new Set(edges.flatMap((e) => [e.from, e.to]));
  const external = [...new Set(edges.map((e) => e.to))].filter((u) => !member.has(u)).sort();
  return {
    type: "flow",
    nodes: [
      ...p.repos
        .filter((r) => touched.has(r.name))
        .map((r) => ({
          id: r.name,
          label: r.part ? `${r.name} · ${r.part}` : r.name,
          // A diagram node's own kind, unrelated to the repository's part: it
          // says whether layoutFlow draws a pill or a box.
          kind: (reached.has(r.name) ? "step" : "start") as FlowNode["kind"],
          src: [],
        })),
      ...external.map((u) => ({ id: u, label: `${u} · library`, kind: "end" as const, src: [] })),
    ],
    edges,
  };
}

/** parkedSummary is the one line standing in for every repository the page is
 * not showing, or null when there is nothing to say.
 *
 * Null rather than an empty string, because nothing disabled is the ordinary
 * case and it has to stay completely quiet — an empty paragraph is still a gap
 * in the layout. Counting lives here rather than in the component so it can be
 * read at every count without rendering.
 *
 * It says how many repositories, how many projects they sit in, and how many of
 * those projects have no enabled member left. That last clause earns its place:
 * a wholly parked project loses its panel AND its name, so nothing else on the
 * page would hint the product exists. A single parked row inside a live project
 * is a smaller absence and gets no special mention.
 *
 * No names. They live in repos.yaml, which is where the flag was set and the
 * only place it can be changed; a list here would be a second copy of the truth
 * with nothing a reader could do about it. */
export type Parked = {
  /** What is missing, and how much of it. Set brighter: it is the scannable half. */
  count: string;
  /** What being parked means. Faint: it is the same every time. */
  note: string;
};

export function parkedSummary(repos: Repo[]): Parked | null {
  const parked = repos.filter((r) => !r.enabled);
  if (parked.length === 0) return null;

  const projectOf = (r: Repo) => r.project || r.name;
  const projects = new Set(parked.map(projectOf));
  const live = new Set(repos.filter((r) => r.enabled).map(projectOf));
  const whole = [...projects].filter((p) => !live.has(p)).length;

  const what =
    parked.length === 1
      ? "1 repository is"
      : projects.size === 1
        ? `${parked.length} repositories in 1 project are`
        : `${parked.length} repositories in ${projects.size} projects are`;
  // "the project entirely" when there is only one and it went whole; "one
  // project entirely" when it is one of several. Both read as English; a single
  // phrase for both does not.
  const entirely =
    whole === 0
      ? ""
      : whole === projects.size && projects.size === 1
        ? ", the project entirely"
        : whole === 1
          ? ", one project entirely"
          : `, ${whole} projects entirely`;
  const they = parked.length === 1 ? "It keeps its index and is" : "They keep their index and are";
  return {
    count: `${what} disabled and not shown${entirely}.`,
    note: `${they} not polled.`,
  };
}

/** unconnected are the members no declared edge touches in either direction.
 * They are listed under the picture rather than drawn into it: nothing in the
 * project reaches them, and that is a fact worth stating — it is what keeps an
 * answer about the storefront from attributing a call to the admin API. */
export function unconnected(p: Project, libraries: Set<string> = new Set()): Repo[] {
  const member = new Set(p.repos.map((r) => r.name));
  const touched = new Set<string>();
  for (const r of p.repos) {
    for (const u of r.uses ?? []) {
      if (member.has(u) || libraries.has(u)) {
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

function Stat({
  label,
  value,
  note,
  title,
  wide,
}: {
  label: string;
  value: string | number;
  note?: string;
  title?: string;
  /** The last stat of an odd count: it takes two columns so the row it
   * closes is full rather than half ruled and half blank. */
  wide?: boolean;
}) {
  return (
    // Every cell draws its own right and bottom rule. The grid around them
    // is pulled a pixel past the wrapper on those two sides, so the rules on
    // the outer edge fall under the wrapper's border instead of doubling it.
    // That holds for any column count and any number of stats, which the
    // even:/last: rules this replaced did not: with eight stats the eighth,
    // spanning two columns as "the odd last one", fell onto a row of its own.
    <div
      className={"border-r border-b border-border px-4 py-3 sm:px-5 sm:py-3.5 " + (wide ? "col-span-2" : "")}
      title={title}
    >
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

  // Everything parked. Kept apart from the empty state above for the same
  // reason "failed" is kept apart from "empty": "nothing is configured" and
  // "all of it is switched off" are different facts with different fixes, and
  // the stat block would otherwise render six zeros with no word of why.
  if (!state.repos.some((r) => r.enabled)) {
    const all = parkedSummary(state.repos)!;
    return (
      <p className="text-muted">
        {all.count} {all.note}
      </p>
    );
  }

  // A parked repository leaves the page entirely — no panel, no row, and out of
  // every count. The stat block describes what is drawn beneath it, so counting
  // what is hidden would leave the totals disagreeing with the panels, which is
  // the one job that block has. The line below carries what the totals dropped.
  const shown = state.repos.filter((r) => r.enabled);
  const parked = parkedSummary(state.repos);
  // A library is a project of one the grouping already yields; it is set
  // apart here so the Projects count says products and the libraries follow
  // them, each saying which products are built on it.
  const libraries = libraryNames(shown);
  const grouped = byProject(shown);
  const projects = grouped.filter((p) => !libraries.has(p.name));
  const shared = grouped.filter((p) => libraries.has(p.name));
  const sum = (pick: (r: Repo) => number) => shown.reduce((n, r) => n + pick(r), 0);
  const lastRun = lastRunAt(shown);

  return (
    <>
      {/* Two-up on a phone, four-up from sm. Never one row: seven or eight
          stats in the 900px column is 112px each, and the Repositories label
          alone needs 145 with its padding, so a single row clipped the last
          stat at the wrapper's edge ("neve|"). Projects leads Repositories:
          the project is what a reader is asked to choose between, and the
          repository count is how that product is built. Libraries is the one
          optional stat, so the count is seven or eight, and only seven leaves
          a row to fill. */}
      <div className="mb-5 overflow-hidden rounded-ui border border-border bg-panel">
      <div className="-mr-px -mb-px grid grid-cols-2 sm:grid-cols-4">
        <Stat label="Projects" value={projects.length} />
        {shared.length > 0 && (
          <Stat
            label="Libraries"
            value={shared.length}
            title="Shared libraries: declared once, used by repositories in any project."
          />
        )}
        {/* No "n active" note any more: with parked rows out of the count there
            is no second number left to give, and the line below says what is
            missing in words the note never had room for. */}
        <Stat label="Repositories" value={shown.length} />
        <Stat label="Files" value={sum((r) => r.files)} />
        <Stat label="Chunks" value={sum((r) => r.chunks)} />
        <Stat label="Modules" value={sum((r) => r.modules)} />
        {/* The commit lane: what "what changed" can look back over. */}
        <Stat
          label="Commits"
          value={sum((r) => r.commits ?? 0)}
          title="Commits recorded for the indexed branches, for questions about what changed."
        />
        {/* The poll, not the index: one health signal for the whole corpus.
            Each row says when its own index was last written. */}
        <Stat
          label="Last poll"
          value={relative(lastRun)}
          title="The last time every repository was fetched and compared with its indexed commit."
          wide={shared.length === 0}
        />
      </div>
      </div>
      {/* Directly under the numbers it explains, and drawn only when there is
          something to explain. Faint, because an absence is not news. */}
      {parked && (
        <p className="mb-5 -mt-2 px-0.5 text-[12.5px] text-faint">
          <span className="text-muted">{parked.count}</span> {parked.note}
        </p>
      )}
      {projects.map((p) => (
        <ProjectPanel key={p.name} project={p} libraries={libraries} />
      ))}
      {shared.map((p) => (
        <ProjectPanel key={p.name} project={p} libraries={libraries} usedBy={usedBy(p.name, shown)} />
      ))}
    </>
  );
}

/** Wiring is the project graph drawn. Nothing while the renderer works and
 * nothing when it refuses the source: the list under it names every
 * repository anyway, and a "could not be drawn" notice belongs to an answer,
 * where the model wrote the picture, not to a page drawing its own config. */
function Wiring({ spec }: { spec: FlowSpec }) {
  const out = useDrawn(toMermaid(spec));
  if (out === null || "error" in out) return null;
  return <MermaidSvg svg={out.svg} title="Project wiring" />;
}

/** ProjectPanel is one product: what it is made of, how its repositories are
 * wired together, and the index status of each.
 *
 * A project of one gets a header and a single row and no wiring, because there
 * is none. That is rongo's own shape and almost every setup's, so it is the
 * case that has to stay quiet. */
function ProjectPanel({
  project,
  libraries,
  usedBy,
}: {
  project: Project;
  libraries: Set<string>;
  /** Set for a library's own panel: the repositories naming it. */
  usedBy?: string[];
}) {
  const spec = wiringSpec(project, libraries);
  const loose = unconnected(project, libraries);
  const sum = (pick: (r: Repo) => number) => project.repos.reduce((n, r) => n + pick(r), 0);

  // The header is the lid of the panel: painted bg-active, the one ground on
  // the page lighter than the panel, so a frame visibly begins where the
  // previous one ended. Before it was bg-bg, the page colour, and the header
  // fused with the gutter above it; with the column headings on bg-bg too the
  // panel opened on two like bands a hairline apart, and the panels stacked
  // as one grey column. The name is serif, the page-title face, so it stops
  // reading as the first mono row. The gap is 28px for the same reason: 20
  // was the gap between two rows of stats, not two products.
  return (
    <section className="mb-7 overflow-hidden rounded-ui border border-border bg-panel">
      <header className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1 border-b border-border bg-active px-3.5 py-2.5">
        <h3 className="font-serif text-[18px] font-medium leading-tight tracking-tight text-ink">
          {project.name}
        </h3>
        <span className="text-[12.5px] text-muted">
          {project.repos.length} {project.repos.length === 1 ? "repository" : "repositories"} ·{" "}
          {sum((r) => r.files)} files · {sum((r) => r.chunks)} chunks
        </span>
        {usedBy && (
          <span className="text-[12.5px] text-muted">
            library
            {usedBy.length > 0 && (
              <>
                {" "}
                · used by <span className="font-mono text-ink-dim">{usedBy.join(", ")}</span>
              </>
            )}
          </span>
        )}
      </header>

      {spec && (
        <div className="border-b border-border px-3.5 pt-3.5 pb-2">
          <Wiring spec={spec} />
          <p className="mt-1.5 text-[12px] text-faint">
            Arrows are <code className="font-mono">uses:</code> entries from{" "}
            <code className="font-mono">repos.yaml</code>. A repository with no arrow is reached from
            outside the project — a browser, a queue, another product. A node marked library is a
            shared repository outside it.
          </p>
          {loose.length > 0 && (
            <p className="mt-1 text-[12px] text-faint">
              No declared connection:{" "}
              <span className="font-mono text-muted">{loose.map((r) => r.name).join(", ")}</span>
            </p>
          )}
        </div>
      )}

      {/* Six columns, and only the first one flexes: every other cell is
          sized to its content (w-px + nowrap), so the numbers on the right
          are always inside the panel and a long repository name wraps
          instead of pushing them out. Below sm the panel still scrolls,
          which is where a 360px phone needs it. */}
      <div className="overflow-x-auto overscroll-x-contain">
        <table className="w-full border-separate border-spacing-0 text-sm">
          <thead>
            <tr className="bg-panel">
              <th className={th + " border-b border-border"}>Repository</th>
              <th className={th + " border-b border-border"}>State</th>
              <th className={th + " border-b border-border"}>Indexed</th>
              <th className={th + " border-b border-border text-right"}>Files</th>
              <th className={th + " border-b border-border text-right"}>Chunks</th>
              <th className={th + " border-b border-border text-right"}>Modules</th>
            </tr>
          </thead>
          <tbody>
            {project.repos.map((r) => {
              const st = rowState(r);
              return (
                <Fragment key={r.name}>
                <tr
                  data-state={st}
                  className={
                    // The bottom border moves to the description row when there
                    // is one, so a repository and its sentence close as a single
                    // box rather than reading as two entries.
                    "align-top [&>td]:border-border-soft " +
                    (r.description ? "" : "[&>td]:border-b last:[&>td]:border-b-0 ") +
                    (r.enabled ? "" : "text-faint")
                  }
                >
                  {/* Name, then part and branch under it: two facts nobody
                      compares down a column, so they cost no column. The name
                      is muted and mono and the project header is ink and
                      serif, which is what makes the header read as the
                      heading. */}
                  <td
                    className={
                      "px-3.5 py-3 " +
                      (r.last_error ? "shadow-[inset_3px_0_0_var(--color-danger)]" : "")
                    }
                  >
                    <span className="font-mono text-muted">{r.name}</span>
                    <div className="mt-1 flex flex-wrap items-center gap-2 font-mono text-[12px] text-faint">
                      {r.part && (
                        <span className="rounded-full bg-active px-2.5 py-0.5 text-[11px] text-ink-dim">
                          {r.part}
                        </span>
                      )}
                      {/* The declared image, beside the part: the two are
                          the entry's own words about what it is, and a
                          release turn reads this one. */}
                      {r.image && <span className="text-faint">{r.image}</span>}
                      {/* A snapshot has no branch anyone chose: rongo made
                          one up to have something to commit to. Where the
                          branch would stand, the line says how the code got
                          here instead, which is the question a branch answers
                          for a clone. It is not under State because a snapshot
                          is not an outcome of the last run. */}
                      {r.snapshot ? (
                        <span
                          className="rounded-full bg-active px-2.5 py-0.5 text-[11px] text-ink-dim"
                          title="Extracted by hand into the repository root. There is no remote, so this commit only changes when the archive is extracted again."
                        >
                          Snapshot
                        </span>
                      ) : (
                        <span>{r.branch}</span>
                      )}
                    </div>
                    {r.last_error && (
                      <div className="mt-1 text-[13px] text-accent-strong">{r.last_error}</div>
                    )}
                  </td>
                  <td className="w-px whitespace-nowrap px-3.5 py-3">
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
                      {/* Unreachable from this page since parked repositories
                          stopped being drawn at all — the line under the stats
                          says how many instead. Kept because rowState still
                          reports "disabled" into data-state, and because the
                          page is one `enabled` filter away from showing them
                          again. */}
                      {!r.enabled && (
                        <span className="rounded-full bg-active px-2.5 py-0.5 text-xs font-medium text-muted">
                          Disabled
                        </span>
                      )}
                    </span>
                  </td>
                  {/* When the index was last written, and at which commit.
                      Not the poll: that moved every half hour whether or not
                      anything was indexed, and read as "indexed just now". */}
                  <td
                    className="w-px whitespace-nowrap px-3.5 py-3"
                    title={`Indexed ${ago(r.last_indexed_at)}. Last poll ${ago(r.last_run_at)}. ${historyLine(r)}`}
                  >
                    {relative(r.last_indexed_at)}
                    {/* Its own line: beside the time it cost the column 70px,
                        which at 1024px with the sidebar open was the 25px
                        that pushed Modules out of the panel. */}
                    <code className="block font-mono text-xs text-faint">{shortSha(r.last_sha)}</code>
                  </td>
                  <td className="w-px whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">
                    {r.files}
                  </td>
                  <td className="w-px whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">
                    {r.chunks}
                  </td>
                  <td className="w-px whitespace-nowrap px-3.5 py-3 text-right font-mono tabular-nums">
                    {r.modules}
                  </td>
                </tr>
                {/* The description gets the whole table width instead of the
                    name column, where 180 characters wrapped to six lines and
                    grew the row while every other cell stayed one line tall.
                    No description, no row: absence takes no marker. */}
                {r.description && (
                  <tr
                    data-state={st}
                    className={
                      "[&>td]:border-b [&>td]:border-border-soft last:[&>td]:border-b-0 " +
                      (r.enabled ? "" : "text-faint")
                    }
                  >
                    {/* Capped: the row has the whole table to spend, and a
                        sentence run across a wide monitor is one long measure
                        nobody reads to the end of. The cell still spans every
                        column so the text starts at the repository name. */}
                    <td colSpan={6} className="px-3.5 pt-0 pb-3 text-[12.5px] text-muted">
                      <div className="max-w-[64rem]">{r.description}</div>
                    </td>
                  </tr>
                )}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

