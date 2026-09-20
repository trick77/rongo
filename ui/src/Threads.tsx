import { useEffect, useState } from "react";

import { Icon } from "./Icon";
import ThreadMenu from "./ThreadMenu";
import { railLabel, railRow } from "./rail";
import { useMenuDismiss } from "./useMenuDismiss";
import { useThreadActions } from "./useThreadActions";

/**
 * How much history the rail carries: ../loom's 30. The rest is on the
 * Threads page, which the foot of the list opens.
 */
export const railLimit = 30;
/**
 * The starred list is not a page: every starred thread, however old, or a
 * star stops doing its job at the 31st. The server's ceiling on one page.
 */
export const starredLimit = 1000;

export type Thread = {
  /**
   * The thread's address, not a row number: 22 URL-safe characters, the same
   * shape as a share token. It is what /thread/… and every /api/threads/…
   * path is written in.
   */
  id: string;
  title: string;
  /**
   * True while the model's title call is still running. The title on such a
   * thread is the question's first words cut at 48 runes — enough for the rail,
   * where it tells one row from another, and not a title: the header says
   * "New question" instead of showing a question cut mid-word.
   */
  title_pending?: boolean;
  created_at: string;
  /** True while a live link points at this thread. */
  shared?: boolean;
  /** True while the reader's star is on it: the rail files it under Starred. */
  starred?: boolean;
};

/** The day group a thread lands in, in the words the rail uses. */
export function group(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "Earlier";
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(now) - day(d)) / 86400000);
  if (diff <= 0) return "Today";
  if (diff === 1) return "Yesterday";
  if (diff < 7) return "This week";
  return d.toLocaleString("en-GB", { month: "long", year: d.getFullYear() === now.getFullYear() ? undefined : "numeric" });
}


/**
 * Reads one page of threads off the list's envelope. Anything that is not
 * one — a stubbed fetch answering [] to every URL, an error page — is an empty
 * list rather than a throw: asking a new question still works, and that is
 * the important path.
 */
export function pageItems(body: unknown): Thread[] {
  if (body && typeof body === "object" && Array.isArray((body as { items?: unknown }).items)) {
    return (body as { items: Thread[] }).items;
  }
  return [];
}

/**
 * The thread list — every starred thread under "Starred", then the latest 30
 * under "Recents". Titles only: the conversation itself is a record, and
 * this is the way back into it after a reload. Everything older is a click
 * away on the Threads page, which the foot of the list opens.
 *
 * Two reads, ../loom's sections with one difference: loom splits its 30 and
 * a starred thread falls off the rail once 30 newer ones exist, which is the
 * one thing a star is for. Here the starred section is its own query, so a
 * star holds however old the thread gets.
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
  busyId = null,
  onList = () => {},
  onDeleted = () => {},
  onRenamed = () => {},
  onShared = () => {},
  onStarred = () => {},
  onAllThreads = () => {},
}: {
  activeId: string | null;
  /** Only ever a real thread: clearing to a new question is the rail's job. */
  onSelect: (id: string) => void;
  version: number;
  busy?: boolean;
  /**
   * The thread being answered, if any. Not always the open one: the rail is
   * live while a turn streams, so the reader can be reading somewhere else
   * entirely. Nothing paints it — an answer is asked at the top of the rail
   * and the row is right there — it only says whose actions to withhold.
   */
  busyId?: string | null;
  /** Reports the loaded list, so the shell can name the open thread. */
  onList?: (list: Thread[]) => void;
  /** A thread is gone. The shell closes it if it was the one on screen. */
  onDeleted?: (id: string) => void;
  /** A thread has a new title; the shell reloads the list. */
  onRenamed?: () => void;
  /** A link was made or taken back; the row markers are stale. */
  onShared?: () => void;
  /** A star was put on or taken off; the shell's copy of the row is stale. */
  onStarred?: () => void;
  /** The foot of the list: the page with every thread on it. */
  onAllThreads?: () => void;
}) {
  // The 30 newest, starred or not, and every starred thread. A row is patched
  // in both, moved between them on a star, and the rail draws the starred
  // list first and the rest of the recent one under it.
  const [threads, setThreads] = useState<Thread[]>([]);
  const [starred, setStarred] = useState<Thread[]>([]);
  // Which row's menu is open. An id rather than an object: the list reloads
  // underneath it.
  const [openMenu, setOpenMenu] = useState<string | null>(null);
  const patch = (id: string, change: Partial<Thread>) => (prev: Thread[]) =>
    prev.map((x) => (x.id === id ? { ...x, ...change } : x));
  const actions = useThreadActions({
    onRenamed: (id, title) => {
      setThreads(patch(id, { title }));
      setStarred(patch(id, { title }));
      onRenamed();
    },
    onDeleted: (id) => {
      setThreads((prev) => prev.filter((x) => x.id !== id));
      setStarred((prev) => prev.filter((x) => x.id !== id));
      onDeleted(id);
    },
    onShared: (id, shared) => {
      // The row's marker follows the link, without waiting for a reload.
      setThreads(patch(id, { shared }));
      setStarred(patch(id, { shared }));
      onShared();
    },
    onStarred: (id, isStarred) => {
      // The row changes section on the spot. Starred is newest first like
      // the rest, so a row joining it is placed by its id's order among the
      // rows already there — a reload would put it in the same place.
      setThreads(patch(id, { starred: isStarred }));
      setStarred((prev) => {
        if (!isStarred) return prev.filter((x) => x.id !== id);
        const row = threads.find((x) => x.id === id);
        if (!row || prev.some((x) => x.id === id)) return prev;
        const next = [...prev, { ...row, starred: true }];
        next.sort((a, b) => (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : 0));
        return next;
      });
      onStarred();
    },
  });

  useMenuDismiss(openMenu !== null, () => setOpenMenu(null));

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [recent, marked] = await Promise.all([
          fetch(`/api/threads?limit=${railLimit}`),
          fetch(`/api/threads?starred=true&limit=${starredLimit}`),
        ]);
        if (!recent.ok) return;
        const list = pageItems(await recent.json());
        const stars = marked.ok ? pageItems(await marked.json()) : [];
        if (!cancelled) {
          setThreads(list);
          setStarred(stars);
          // The union, so the shell can name an open thread from either.
          const ids = new Set(list.map((t) => t.id));
          onList([...list, ...stars.filter((t) => !ids.has(t.id))]);
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

  // Recents is the 30 less the starred ones, which sit in their own section
  // above; the day groups split what is left.
  const starredIds = new Set(starred.map((t) => t.id));
  const groups: { label: string; items: Thread[] }[] = [];
  for (const t of threads) {
    if (starredIds.has(t.id)) continue;
    const label = group(t.created_at);
    const last = groups[groups.length - 1];
    if (last && last.label === label) last.items.push(t);
    else groups.push({ label, items: [t] });
  }

  const item =
    // ../loom's row: hover only moves the ground, never the text — the title
    // is already at its reading brightness. The hover ground itself lives on
    // the idle branch below, so it cannot lift the selected row's darker one.
    //
    // The 28px pitch is the rhythm on every pointer. A touch screen used to
    // get a 44px row, but it left the rail's own spacing — tuned against 28 —
    // standing around it, and an iPad read far too airy for it. What the 28px
    // does demand is that the whole of it be tappable: see the title button.
    //
    // Nothing here dims. A running turn no longer closes the rail — any thread
    // can be opened while an answer is being written elsewhere.
    "flex h-7 w-full items-center gap-2 rounded-md pr-1 pl-1.5 text-left text-sm/5";

  const row = (t: Thread) => {
                const active = t.id === activeId;
                // The thread this turn is being written into. Its actions are
                // the only ones a running turn withholds.
                const writing = busy && t.id === busyId;
                // Gated on the writing row as well as on the id: the trigger
                // is dropped the moment that thread's turn starts, and a menu
                // left standing over it would still offer Delete on the
                // thread being written.
                const menuOpen = openMenu === t.id && !writing;
                return (
                  <li key={t.id} className="relative min-w-0">
                    <div
                      className={
                        item + " group " + (active ? "bg-rail-sel text-white" : "text-rail hover:bg-rail-hover")
                      }
                    >
                      <button
                        type="button"
                        aria-current={active ? "true" : undefined}
                        onClick={() => onSelect(t.id)}
                        // self-stretch, and it is not cosmetic: without it the
                        // button is a flex child under items-center and shrinks
                        // to its 20px line box inside a 28px row, leaving a 4px
                        // dead band along the top and bottom of every row that
                        // selects nothing. On a finger that is a third of the
                        // row, and on the topmost row — where the 20px above it
                        // is the group's own margin — a tap that lands high
                        // hits nothing at all, while the same miss further down
                        // lands on the row above and at least does something.
                        // The button paints no ground of its own, so filling
                        // the row moves not one pixel.
                        // items-center keeps the title on the line it was on:
                        // a 28px button holding a 20px line would otherwise
                        // set the text 4px higher than it sits today.
                        className="relative flex min-w-0 flex-1 items-center self-stretch overflow-hidden text-left whitespace-nowrap"
                      >
                        {/* ../loom's sidebar truncates in two ways, and which
                            one is showing IS the selection. An idle row ends
                            in an ellipsis: the row paints no ground of its own
                            against the panel, and a gradient needs a colour to
                            arrive at. A selected row drops the ellipsis for
                            pr-7 — reserving the kebab's 24px, which is visible
                            from here on — and runs the title out under a fade
                            to the selected ground instead, so the text stays
                            whole under the one row the reader is reading.
                            The hover ground gets no fade: the row is still
                            idle, and loom leaves its ellipsis alone. */}
                        <span className={"block " + (active ? "pr-7" : "truncate")}>{t.title}</span>
                        {active && (
                          <span
                            aria-hidden="true"
                            className="pointer-events-none absolute inset-y-0 right-0 w-9 bg-gradient-to-r from-transparent to-rail-sel"
                          />
                        )}
                      </button>
                      {/* No dot on the row being written: a question is asked
                          at the top of the rail and its row is the one right
                          there, under the actions, so a marker beside it says
                          what the reader just did. The one case it would speak
                          for — a follow-up asked in an older thread, whose row
                          keeps its place down the list — is one the composer
                          already accounts for, and it is not worth a mark on
                          every other turn. */}
                      {/* A live link is a different kind of fact: it is true
                          of the thread until somebody revokes it, not for the
                          length of a turn, and it is the only way to see from
                          the rail that a conversation is readable by people
                          who are not here. A dot, not a word — the row is 28px
                          of pitch on a 362px rail, and a "Shared" pill would
                          eat the title. The accent orange, not the status
                          line's green: green on the rail reads as "indexed",
                          and a link handed out is a different fact. */}
                      {t.shared && (
                        <span
                          title="Shared with a link"
                          className="h-[7px] w-[7px] shrink-0 self-center rounded-full bg-accent-strong"
                        >
                          <span className="sr-only">Shared</span>
                        </span>
                      )}
                      {/* No actions on the thread being written: deleting it
                          would pull the record out from under the answer still
                          landing on it. Every other row keeps its own. */}
                      {!writing && (
                        <button
                          type="button"
                          aria-haspopup="menu"
                          aria-expanded={menuOpen}
                          aria-label={"Actions for " + t.title}
                          onClick={() => setOpenMenu(menuOpen ? null : t.id)}
                          // Quiet on an idle row, but never unreachable: it
                          // comes back for the keyboard and on touch, where
                          // there is no hover to reveal it.
                          //
                          // The 24px square is the paint; the tap box reaches
                          // the row's full 28px through the after: rectangle,
                          // for the same reason the title does. Extending the
                          // square itself would enlarge the hover ground with
                          // it, and that IS paint.
                          className={
                            "relative grid h-6 w-6 shrink-0 place-items-center rounded-md text-rail transition-colors after:absolute after:inset-x-0 after:-inset-y-0.5 after:content-[''] hover:bg-active hover:text-ink " +
                            (active || menuOpen
                              ? ""
                              : "invisible group-hover:visible group-focus-within:visible [@media(hover:none)]:visible")
                          }
                        >
                          <Icon name="moreVertical" size="18px" />
                        </button>
                      )}
                    </div>
                    {menuOpen && (
                      <ThreadMenu
                        starred={t.starred}
                        onStar={() => {
                          setOpenMenu(null);
                          actions.startStar(t);
                        }}
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
  };

  return (
    <nav aria-label="Threads" className="flex flex-1 flex-col">
      {/* No scroller of its own: the rail (App.tsx) scrolls as a whole. flex-1
          stays so a short history still leaves the index line at the foot. */}
      <div className="px-2 pb-4">
        {/* ../loom's sections, on its SidebarSection metrics: 20px above a
            label, 8px below, the label in the rail's 12/16. Starred only once
            there is a star — a heading over nothing would promise a section
            the reader has not made yet. */}
        {starred.length > 0 && (
          <section>
            <h3 className={"mt-5 mb-2 " + railLabel}>Starred</h3>
            {/* No gap: the 28px row pitch is the rhythm, as ../loom has it. */}
            <ul className="flex flex-col">{starred.map(row)}</ul>
          </section>
        )}
        {groups.length > 0 && (
          <section>
            {/* "Recents" heads today's threads, which carry no day label of
                their own: naming the day a thread was asked on is only worth
                the line once the day is no longer this one. With nothing from
                today the first day label heads the section itself — a
                "Recents" over a "Yesterday" is two labels stacked on the same
                rows, and it read as a Recents group with nothing in it the
                morning after the day's only thread was deleted. */}
            {groups[0].label === "Today" && <h3 className={"mt-5 mb-2 " + railLabel}>Recents</h3>}
            {groups.map((g) => (
              // Every painted day label carries the 20px a section heading
              // does: it is one, whether it follows today's rows or Starred.
              <div key={g.label}>
                {g.label !== "Today" && <h3 className={"mt-5 mb-2 " + railLabel}>{g.label}</h3>}
                <ul className="flex flex-col">{g.items.map(row)}</ul>
              </div>
            ))}
          </section>
        )}
        {/* The foot, ../loom's Sidebar: the way to every thread, painted like
            the actions at the top of the rail and never marked current — it
            is a door, not a place. Only once there is history to be more of:
            an empty rail with "All threads" under it would promise a page
            with nothing on it. */}
        {threads.length > 0 && (
          <button
            type="button"
            onClick={onAllThreads}
            className={railRow + " mt-1.5 text-rail hover:bg-rail-hover"}
          >
            <span className="grid h-5 w-5 shrink-0 place-items-center">
              <Icon name="allThreads" size="21px" className="text-ink-dim" />
            </span>
            All threads
          </button>
        )}
      </div>
      {actions.dialogs}
    </nav>
  );
}
