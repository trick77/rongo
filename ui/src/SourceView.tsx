import { useEffect, useMemo, useRef, useState } from "react";
import { highlightLines, languageForPath } from "./highlight";

/** What a source row knows about the file it points at. The commit is
 * optional because citations recorded before it travelled with them have
 * none; the server then reads the file at the commit it was indexed at. */
export type SourceRef = {
  marker: number;
  repo: string;
  branch: string;
  path: string;
  start_line: number;
  end_line: number;
  sha?: string;
};

type Loaded =
  | { state: "loading" }
  | { state: "error"; message: string }
  | { state: "ready"; sha: string; branch: string; lines: string[] };

/** How many lines of context sit above the cited range when the viewer opens.
 * Enough to see the enclosing declaration, not enough to lose the range. */
const contextAbove = 3;

/**
 * SourceView opens a cited file out of rongo's own checkout, at the commit the
 * answer was written from, with the cited lines marked. An overlay rather
 * than a page: the reader is checking a claim and goes straight back to the
 * answer.
 */
export default function SourceView({ source, onClose }: { source: SourceRef; onClose: () => void }) {
  const [loaded, setLoaded] = useState<Loaded>({ state: "loading" });
  const closeButton = useRef<HTMLButtonElement>(null);
  const anchor = useRef<HTMLDivElement>(null);

  // Focus moves into the dialog on open and back to where it was on close,
  // so a keyboard reader does not land at the top of the page afterwards.
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    closeButton.current?.focus();
    return () => before?.focus?.();
  }, []);

  // Escape closes. Tab stays inside: the dialog is modal, and the close button
  // is its only control, so a Tab that left it would land in the dimmed page
  // behind the overlay.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
      if (e.key === "Tab") {
        e.preventDefault();
        closeButton.current?.focus();
      }
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  useEffect(() => {
    let cancelled = false;
    setLoaded({ state: "loading" });
    (async () => {
      try {
        const q = new URLSearchParams({ repo: source.repo, path: source.path, sha: source.sha ?? "" });
        const res = await fetch(`/api/source?${q}`);
        if (cancelled) return;
        if (!res.ok) {
          // The server's message is written for the reader (not in the
          // checkout, binary, too large); a bare status is not.
          const message = (await res.text()).trim() || `The server answered with ${res.status}.`;
          if (!cancelled) setLoaded({ state: "error", message });
          return;
        }
        const file = await res.json();
        if (cancelled) return;
        const content: string = file.content ?? "";
        const lines = content.split("\n");
        // A file ending in a newline has no empty last line to show.
        if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
        setLoaded({ state: "ready", sha: file.sha ?? "", branch: file.branch ?? source.branch, lines });
      } catch {
        if (!cancelled) setLoaded({ state: "error", message: "The connection was lost." });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [source]);

  // Once the file is there, the cited range comes into view with a little
  // context above it. Guarded like the thread's own scroll: jsdom has no
  // scrollIntoView.
  useEffect(() => {
    if (loaded.state === "ready") anchor.current?.scrollIntoView?.({ block: "start" });
  }, [loaded.state]);

  const slash = source.path.lastIndexOf("/");
  const dir = slash >= 0 ? source.path.slice(0, slash + 1) : "";
  const base = source.path.slice(slash + 1);
  const anchorLine = Math.max(source.start_line - contextAbove, 1);
  const shortSha = (loaded.state === "ready" ? loaded.sha : source.sha ?? "").slice(0, 7);
  const branch = loaded.state === "ready" ? loaded.branch : source.branch;
  // A citation without its own commit is read at the commit the file was
  // last indexed at, which can be newer than the answer. If the file has
  // shrunk past the cited range since, nothing would be marked and the
  // reader would take the answer for unbacked. Say what happened instead.
  const moved = loaded.state === "ready" && source.start_line > loaded.lines.length;
  // Coloured once per file, not per render: the grammar is the file's, and
  // the per-line grid below keeps its numbers, anchors and cited-range mark.
  const lines = loaded.state === "ready" ? loaded.lines : null;
  const coloured = useMemo(
    () => (lines ? highlightLines(lines.join("\n"), languageForPath(source.path)) : []),
    [lines, source.path],
  );

  return (
    <div
      // Edge to edge on a phone: 24px of scrim on each side buys nothing when
      // the code inside is already scrolling sideways.
      className="fixed inset-0 z-30 flex items-center justify-center bg-black/55 p-0 sm:p-6 md:p-10"
      // The pointer, not the mouse: iOS Safari does not deliver mouse events
      // to a plain div, and an iPad has no Escape key to fall back on.
      onPointerDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={`Source ${source.marker}: ${source.path}`}
        className="grid h-full w-full max-w-[1100px] grid-rows-[auto_1fr] overflow-hidden rounded-none border-0 bg-panel shadow-panel sm:rounded-ui-lg sm:border sm:border-elevated-border"
      >
        <header className="flex items-center gap-2 border-b border-border px-3 py-2.5 sm:gap-3.5 sm:px-4.5 sm:py-3">
          <span className="font-mono font-semibold text-accent-strong">{source.marker}</span>
          <span className="min-w-0 truncate font-mono text-[13.5px] text-muted">
            {source.repo} · {dir}
            <b className="font-medium text-ink">{base}</b>
          </span>
          {/* Out of sight on a phone, where the filename is what the reader
              needs from a 360px header; the branch, the sha and the range are
              all in the citation list under the answer as well. */}
          <span className="ml-auto hidden shrink-0 items-center gap-2.5 font-mono text-[11.5px] text-faint sm:flex">
            <span className="rounded-full border border-border px-2 py-px">{branch}</span>
            {shortSha && <span className="rounded-full border border-border px-2 py-px">{shortSha}</span>}
            <span>
              lines {source.start_line}–{source.end_line}
            </span>
          </span>
          <button
            ref={closeButton}
            type="button"
            onClick={onClose}
            aria-label="Close"
            // ml-auto only where the pills beside it are not rendered — with
            // both carrying it, flexbox splits the free space and the pills
            // drift away from the button.
            className="ml-auto grid h-11 w-11 place-items-center rounded-ui-sm text-lg leading-none text-muted hover:bg-active hover:text-ink sm:ml-0 sm:h-8 sm:w-8"
          >
            ×
          </button>
        </header>

        <div className="min-h-0 overflow-auto py-2.5 font-mono text-[12.5px] leading-[1.55]">
          {loaded.state === "loading" && <p className="px-5 py-3 text-muted">Reading the file…</p>}
          {loaded.state === "error" && (
            <p role="alert" className="px-5 py-3 text-muted">
              {loaded.message}
            </p>
          )}
          {moved && (
            <p role="status" className="mx-5 my-2 rounded-ui-sm border border-border bg-active px-3 py-2 text-muted">
              The file has changed since the answer was written: it has {loaded.lines.length} lines at this
              commit, and the cited range starts at line {source.start_line}.
            </p>
          )}
          {loaded.state === "ready" &&
            loaded.lines.map((line, i) => {
              const n = i + 1;
              const hit = n >= source.start_line && n <= source.end_line;
              return (
                <div
                  key={n}
                  ref={n === anchorLine ? anchor : undefined}
                  data-line={n}
                  data-hit={hit || undefined}
                  className={
                    // 40px still holds a four-digit line number and gives the
                    // code back 16px of a 360px screen.
                    "grid grid-cols-[40px_1fr] sm:grid-cols-[56px_1fr] border-l-2 whitespace-pre " +
                    (hit ? "border-accent bg-accent-dim/35" : "border-transparent")
                  }
                >
                  <span
                    aria-hidden="true"
                    className={"pr-2 text-right select-none sm:pr-4 " + (hit ? "text-accent-strong" : "text-faint")}
                  >
                    {n}
                  </span>
                  <span className="pr-5 text-ink-dim">{coloured[i] ?? line}</span>
                </div>
              );
            })}
        </div>
      </div>
    </div>
  );
}
