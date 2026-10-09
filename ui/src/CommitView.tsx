import { isoDay, shortSha, splitPath } from "./paths";
import { useLoaded } from "./hooks";
import { fetchJSON } from "./http";
import OverlayShell, { overlayClose } from "./OverlayShell";
import { type SourceRef } from "./SourceView";

/** One file a commit touched, as the server reports it. indexed says whether
 * the source viewer can open it: a path the indexer skipped, or one the
 * commit deleted, has nothing to show. */
type FileChange = { path: string; added: number; deleted: number; indexed: boolean };

/**
 * CommitView opens a cited commit: its message, its date and the files it
 * touched. It is to a commit citation what SourceView is to a file citation,
 * and it is drawn as the same overlay: the reader is checking a claim about a
 * change and goes straight back to the answer.
 *
 * No author anywhere on it: the view is reachable from a shared page.
 */
export default function CommitView({
  source,
  onClose,
  onOpenFile,
  endpoint = "/api/commit",
}: {
  source: SourceRef;
  onClose: () => void;
  /** Opens one touched file in the source viewer, at the commit the file was
   * last indexed at. Left out on a share page, whose file endpoint serves
   * only what a turn cites; the paths are then plain text. */
  onOpenFile?: (ref: SourceRef) => void;
  /** Where the commit is read from; the share page passes its own. */
  endpoint?: string;
}) {

  const [loaded] = useLoaded(async () => {
    const q = new URLSearchParams({ repo: source.repo, sha: source.sha ?? "" });
    const c = await fetchJSON(`${endpoint}?${q}`);
    return {
      sha: (c.sha ?? "") as string,
      branch: (c.branch ?? source.branch) as string,
      committed_at: (c.committed_at ?? "") as string,
      subject: (c.subject ?? "") as string,
      body: (c.body ?? "") as string,
      files: (Array.isArray(c.files) ? c.files : []) as FileChange[],
    };
  }, [source, endpoint]);

  const sha = shortSha(loaded.state === "ready" ? loaded.sha : source.sha);
  const branch = loaded.state === "ready" ? loaded.branch : source.branch;
  const subject = loaded.state === "ready" ? loaded.subject : source.subject ?? "";
  const day = isoDay(loaded.state === "ready" ? loaded.committed_at : source.committed_at);

  return (
    <OverlayShell
      label={`Commit ${source.marker}: ${subject}`}
      dialogClassName="grid h-full w-full max-w-[900px] grid-cols-[minmax(0,1fr)] grid-rows-[auto_1fr] overflow-hidden rounded-none border-0 bg-panel shadow-panel sm:h-auto sm:max-h-full sm:rounded-ui-lg sm:border sm:border-elevated-border"
      closeClassName={"ml-auto " + overlayClose + " sm:ml-0"}
      onClose={onClose}
      header={
        <>
          <span className="font-mono font-semibold text-accent-strong">{source.marker}</span>
          <span className="min-w-0 truncate text-[13.5px] text-muted">
            {source.repo} · <b className="font-medium text-ink">{subject}</b>
          </span>
          <span className="ml-auto hidden shrink-0 items-center gap-2.5 font-mono text-[11.5px] text-faint sm:flex">
            <span className="rounded-full border border-border px-2 py-px">{branch}</span>
            {sha && <span className="rounded-full border border-border px-2 py-px">{sha}</span>}
            {day && <span>{day}</span>}
          </span>
        </>
      }
    >
      {/* In the Tab ring, so a long message or file list scrolls by keyboard. */}
      <div tabIndex={0} className="min-h-0 overflow-auto px-5 py-4 text-[13.5px] leading-[1.55]">
        {loaded.state === "loading" && <p className="text-muted">Reading the commit…</p>}
        {loaded.state === "error" && (
          <p role="alert" className="text-muted">
            {loaded.message}
          </p>
        )}
        {loaded.state === "ready" && (
          <>
            {/* The message as the author wrote it: pre-wrap keeps the
                paragraphs and the bullet lists a commit body carries. */}
            {loaded.body && <p className="mb-4 whitespace-pre-wrap text-ink-dim">{loaded.body}</p>}
            <h2 className="mb-1.5 text-[11px] font-medium uppercase tracking-[.12em] text-faint">
              {loaded.files.length === 1 ? "1 file" : `${loaded.files.length} files`}
            </h2>
            <ul className="font-mono text-xs">
              {loaded.files.map((f) => {
                const { dir, base } = splitPath(f.path);
                // A path the viewer can open is a button; one it cannot
                // (skipped, deleted, or on a share page) is text.
                return (
                  <li key={f.path} className="flex items-baseline border-b border-border-soft py-1.5">
                    {onOpenFile && f.indexed ? (
                      <button
                        type="button"
                        onClick={() =>
                          onOpenFile({ marker: source.marker, repo: source.repo, branch, path: f.path, start_line: 1, end_line: 0 })
                        }
                        className="group min-w-0 break-all text-left text-muted"
                      >
                        {dir}
                        <b className="font-medium text-ink-dim underline-offset-[3px] group-hover:underline group-hover:decoration-accent">
                          {base}
                        </b>
                      </button>
                    ) : (
                      <span className="min-w-0 break-all text-muted">
                        {dir}
                        <b className="font-medium text-ink-dim">{base}</b>
                      </span>
                    )}
                    <span className="ml-2 shrink-0 text-faint">
                      <span className="text-accent-strong">+{f.added}</span> <span>-{f.deleted}</span>
                    </span>
                  </li>
                );
              })}
            </ul>
          </>
        )}
      </div>
    </OverlayShell>
  );
}
