import { Fragment, useCallback, useEffect, useState, type ReactNode } from "react";

import { Icon } from "./Icon";
import ThreadMenu from "./ThreadMenu";
import { pageItems, type Thread } from "./Threads";
import { useInfiniteList, type Page } from "./useInfiniteList";
import { useThreadActions } from "./useThreadActions";

/** A thread the search found and, when found by a message, the passage. */
export type Hit = Thread & {
  /** The matching passage, matches between « and », … where it was cut. */
  snippet?: string;
};

// ../loom's figures. Fifty a page for the scroll; a search answers up to 200
// in one go and is not paged, bounded only to keep one render cheap.
export const pageSize = 50;
export const searchLimit = 200;
export const searchDebounceMs = 250;

/**
 * Every thread, searchable — what the rail's 30 are a cut of. ../loom's
 * ThreadsPage without its select mode, stars and projects: rongo's threads
 * have none of those.
 *
 * Two lists share the surface. With the box empty it is the whole history,
 * newest first, fifty at a time as the reader scrolls. With a term in it, it
 * is every thread the term is found in — by title first, then by what was
 * said in it, each of those with the passage under the title — in one answer.
 */
export default function ThreadsPage({
  activeId,
  version,
  onSelect,
  onChanged,
  onDeleted,
}: {
  activeId: string | null;
  /** Bumped by the shell whenever the list may have changed; reloads it. */
  version: number;
  onSelect: (id: string) => void;
  /** A title or a link changed here; the rail is stale. */
  onChanged: () => void;
  /** A thread is gone. The shell closes it if it was the one on screen. */
  onDeleted: (id: string) => void;
}) {
  const [input, setInput] = useState("");
  const [term, setTerm] = useState("");
  const [openMenu, setOpenMenu] = useState<string | null>(null);

  // The box is debounced into the term that reaches the API.
  useEffect(() => {
    const handle = window.setTimeout(() => setTerm(input.trim()), searchDebounceMs);
    return () => window.clearTimeout(handle);
  }, [input]);

  const fetchPage = useCallback(
    async (cursor: string | null): Promise<Page<Thread>> => {
      const q = new URLSearchParams({ limit: String(pageSize) });
      if (cursor) q.set("cursor", cursor);
      const res = await fetch(`/api/threads?${q}`);
      if (!res.ok) throw new Error(String(res.status));
      const body: unknown = await res.json();
      const next = (body as { next_cursor?: unknown })?.next_cursor;
      return { items: pageItems(body), next_cursor: typeof next === "string" ? next : null };
    },
    [],
  );
  // The search box empties the scrolled list too: a mutation (a rename or a
  // delete from a row's menu, via version) reloads whichever list is showing.
  const searching = term !== "";
  const list = useInfiniteList(fetchPage, [searching, version]);

  const [search, setSearch] = useState<{ term: string; hits: Hit[]; failed: boolean } | null>(null);
  useEffect(() => {
    if (!searching) {
      setSearch(null);
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch(`/api/threads/search?q=${encodeURIComponent(term)}&limit=${searchLimit}`);
        if (!res.ok) throw new Error(String(res.status));
        const body: unknown = await res.json();
        // Answered for the term that was asked, never for a term that was
        // typed over while the request was out.
        if (!cancelled) setSearch({ term, hits: pageItems(body) as Hit[], failed: false });
      } catch {
        if (!cancelled) setSearch({ term, hits: [], failed: true });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [term, searching, version]);

  const actions = useThreadActions({
    onRenamed: (id, title) => {
      list.setItems((prev) => prev.map((x) => (x.id === id ? { ...x, title } : x)));
      setSearch((prev) => prev && { ...prev, hits: prev.hits.map((x) => (x.id === id ? { ...x, title } : x)) });
      onChanged();
    },
    onDeleted: (id) => {
      list.setItems((prev) => prev.filter((x) => x.id !== id));
      setSearch((prev) => prev && { ...prev, hits: prev.hits.filter((x) => x.id !== id) });
      onDeleted(id);
    },
    onShared: (id, shared) => {
      list.setItems((prev) => prev.map((x) => (x.id === id ? { ...x, shared } : x)));
      setSearch((prev) => prev && { ...prev, hits: prev.hits.map((x) => (x.id === id ? { ...x, shared } : x)) });
      onChanged();
    },
  });

  // A menu closes on a pointer anywhere but itself and the kebabs, as the
  // rail's does.
  useEffect(() => {
    if (openMenu === null) return;
    function onPointerDown(e: PointerEvent) {
      const target = e.target;
      if (target instanceof Element && target.closest('[role="menu"], [aria-haspopup="menu"]')) return;
      setOpenMenu(null);
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") setOpenMenu(null);
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [openMenu]);

  // The search's answer is only shown once it is for the term in the box:
  // until then the previous list would flash under a term it has nothing to
  // do with.
  const rows: Hit[] = searching ? (search?.term === term ? search.hits : []) : list.items;
  const settled = searching ? search?.term === term : list.loaded;
  const failed = searching ? search?.failed === true : list.failed;

  return (
    <div>
      <div className="relative">
        <Icon
          name="search"
          size="18px"
          className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-faint"
        />
        {/* 16px on a touch screen rather than 15, or iOS zooms the page in
            on focus, as the composer has it. */}
        <input
          type="text"
          autoFocus
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Search threads…"
          aria-label="Search threads"
          className="h-11 w-full rounded-ui border border-border bg-bg pr-3 pl-11 text-[15px] text-ink outline-none focus:border-accent pointer-coarse:text-base"
        />
      </div>

      {failed && (
        <p role="alert" className="mt-4 text-accent-strong">
          The threads cannot be fetched.
        </p>
      )}

      <ul className="mt-3">
        {rows.length === 0 && !failed && settled && (
          <li className="py-10 text-center text-muted">{searching ? "No thread matches." : "No threads yet."}</li>
        )}
        {rows.map((t) => {
          const active = t.id === activeId;
          const menuOpen = openMenu === t.id;
          return (
            <li key={t.id} className="relative border-b border-border-soft last:border-b-0">
              <div
                className={
                  "group flex items-center gap-3 rounded-ui-sm px-3 " +
                  (t.snippet ? "min-h-[60px] py-2 " : "min-h-[49px] ") +
                  (active ? "bg-active" : "hover:bg-active")
                }
              >
                <button
                  type="button"
                  aria-current={active ? "true" : undefined}
                  onClick={() => onSelect(t.id)}
                  className="flex min-w-0 flex-1 flex-col items-start self-stretch justify-center text-left"
                >
                  <span className="flex w-full min-w-0 items-center gap-2">
                    <span className="truncate text-[15px] text-ink">
                      {searching ? mark(t.title, term) : t.title}
                    </span>
                    {t.shared && (
                      <span title="Shared with a link" className="h-[7px] w-[7px] shrink-0 rounded-full bg-online">
                        <span className="sr-only">Shared</span>
                      </span>
                    )}
                  </span>
                  {t.snippet && (
                    <span className="mt-0.5 w-full truncate text-[13px] text-muted">{snippet(t.snippet)}</span>
                  )}
                </button>
                <span className="shrink-0 text-[13px] whitespace-nowrap text-muted">{when(t.created_at)}</span>
                <button
                  type="button"
                  aria-haspopup="menu"
                  aria-expanded={menuOpen}
                  aria-label={"Actions for " + t.title}
                  onClick={() => setOpenMenu(menuOpen ? null : t.id)}
                  className={
                    "grid h-7 w-7 shrink-0 place-items-center rounded-md text-muted transition-colors hover:bg-elevated hover:text-ink " +
                    (menuOpen ? "" : "invisible group-hover:visible group-focus-within:visible [@media(hover:none)]:visible")
                  }
                >
                  <Icon name="moreVertical" size="17px" />
                </button>
              </div>
              {menuOpen && (
                <ThreadMenu
                  onShare={() => {
                    setOpenMenu(null);
                    actions.startShare(t);
                  }}
                  onRename={() => {
                    setOpenMenu(null);
                    actions.startRename(t);
                  }}
                  onDelete={() => {
                    setOpenMenu(null);
                    actions.startDelete(t);
                  }}
                />
              )}
            </li>
          );
        })}
      </ul>
      {/* The sentinel the scroll is watched through; the next page loads
          when it comes into view. Not while searching, which is one answer. */}
      {!searching && <div ref={list.sentinelRef} aria-hidden="true" className="h-px" />}
      {!searching && list.loadingMore && list.hasMore && (
        <p className="mt-3 px-3 text-[13px] text-muted">Loading more…</p>
      )}
      {actions.dialogs}
    </div>
  );
}

/** The day a thread was asked on, as a row shows it: "17 Aug", "17 Aug 2025". */
export function when(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString("en-GB", {
    day: "numeric",
    month: "short",
    year: d.getFullYear() === now.getFullYear() ? undefined : "numeric",
  });
}

/**
 * The title with every occurrence of every term in bold, each term matched on
 * its own as a substring — the same rule the title search applies.
 */
export function mark(text: string, query: string): ReactNode {
  const terms = query
    .trim()
    .split(/\s+/)
    .filter((t) => t.length > 0)
    .map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  if (terms.length === 0) return text;
  // One alternation, so adjacent matches keep their order; the capture puts
  // the matched runs at the odd indices of the split.
  const parts = text.split(new RegExp(`(${terms.join("|")})`, "ig"));
  return parts.map((part, i) =>
    i % 2 === 1 ? (
      <strong key={i} className="font-semibold">
        {part}
      </strong>
    ) : (
      <Fragment key={i}>{part}</Fragment>
    ),
  );
}

/**
 * The passage the search found, its «matches» in bold. Never innerHTML: the
 * passage is model output, and a tag in it would be a tag on the page.
 *
 * The snippet centres the match, and the line it goes on is one truncated
 * span: too long a lead pushes the match off the right edge, none loses the
 * words that make it readable. The lead is cut to a bounded length at a
 * word, as ../loom cuts it.
 */
export function snippet(raw: string): ReactNode {
  let text = raw;
  const first = text.indexOf("«");
  const lead = 32;
  if (first > lead) {
    let cut = first - lead;
    const space = text.indexOf(" ", cut);
    if (space !== -1 && space < first) cut = space + 1;
    text = "…" + text.slice(cut);
  }
  const parts = text.split(/«(.*?)»/g);
  return parts.map((part, i) =>
    i % 2 === 1 ? (
      <strong key={i} className="font-semibold text-ink-dim">
        {part}
      </strong>
    ) : (
      <Fragment key={i}>{part}</Fragment>
    ),
  );
}
