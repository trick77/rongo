import { useEffect, useState } from "react";

import { Icon } from "./Icon";
import ThreadMenu from "./ThreadMenu";
import { DeleteThreadModal, RenameThreadModal } from "./ThreadModals";

/**
 * The rail's label size, ../loom's: 12/16 in sentence case, not an uppercase
 * tracked eyebrow. The day groups below are the only thing wearing it — the
 * rail has no heading of its own, the titles stand alone.
 */
const railLabel = "px-1.5 text-xs/4 text-rail-label";

export type Thread = {
  id: number;
  title: string;
  /**
   * True while the model's title call is still running. The title on such a
   * thread is the question's first words cut at 48 runes — enough for the rail,
   * where it tells one row from another, and not a title: the header says
   * "New question" instead of showing a question cut mid-word.
   */
  title_pending?: boolean;
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
  busyId = null,
  onList = () => {},
  onDeleted = () => {},
  onRenamed = () => {},
}: {
  activeId: number | null;
  /** Only ever a real thread: clearing to a new question is the rail's job. */
  onSelect: (id: number) => void;
  version: number;
  busy?: boolean;
  /**
   * The thread being answered, if any. Not always the open one: the rail is
   * live while a turn streams, so the reader can be reading somewhere else
   * entirely. Nothing paints it — an answer is asked at the top of the rail
   * and the row is right there — it only says whose actions to withhold.
   */
  busyId?: number | null;
  /** Reports the loaded list, so the shell can name the open thread. */
  onList?: (list: Thread[]) => void;
  /** A thread is gone. The shell closes it if it was the one on screen. */
  onDeleted?: (id: number) => void;
  /** A thread has a new title; the shell reloads the list. */
  onRenamed?: () => void;
}) {
  const [threads, setThreads] = useState<Thread[]>([]);
  // Which row's menu is open, and which thread a dialog is asking about.
  // Both are ids rather than objects: the list reloads underneath them.
  const [openMenu, setOpenMenu] = useState<number | null>(null);
  const [renaming, setRenaming] = useState<Thread | null>(null);
  const [deleting, setDeleting] = useState<Thread | null>(null);
  const [pending, setPending] = useState(false);

  // The menu closes on a pointer anywhere but the menu itself and the kebabs
  // — another row's title included, which switches thread and would otherwise
  // leave the menu hanging off the row that was left. The kebabs are spared
  // because their own click toggles, and closing here first would make the
  // toggle reopen the menu that was just dismissed.
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

  /**
   * Both actions answer 204 and carry nothing back, so the row is dropped or
   * the list reloaded from what was asked for rather than from a response
   * body. A failure leaves the dialog up: the row is still there, and telling
   * someone their thread is gone when it is not is worse than saying nothing.
   */
  async function rename(t: Thread, title: string) {
    setPending(true);
    try {
      const res = await fetch(`/api/threads/${t.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title }),
      });
      if (!res.ok) return;
      setThreads((prev) => prev.map((x) => (x.id === t.id ? { ...x, title } : x)));
      setRenaming(null);
      onRenamed();
    } catch {
      // Nothing to say: the title on screen is still the stored one.
    } finally {
      setPending(false);
    }
  }

  async function remove(t: Thread) {
    setPending(true);
    try {
      const res = await fetch(`/api/threads/${t.id}`, { method: "DELETE" });
      if (!res.ok) return;
      setThreads((prev) => prev.filter((x) => x.id !== t.id));
      setDeleting(null);
      onDeleted(t.id);
    } catch {
      // Same: the row stays, and the dialog with it.
    } finally {
      setPending(false);
    }
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

  return (
    <nav aria-label="Threads" className="flex min-h-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 overflow-auto px-2 pb-4">
        {groups.map((g) => (
          // "Today" is not painted: it always heads the list, and naming the
          // day a thread was asked on is only worth the line once the day is
          // no longer this one. The group still exists, so tomorrow's threads
          // split off it.
          //
          // Unpainted, it has to carry the gap from the actions above the
          // scroller itself: mt-5, the 20px a rail section gets. A painted
          // first group reaches the same 20 through its own h3, so the list
          // starts on one rhythm whichever case a morning falls into.
          <div key={g.label} className={g.label === "Today" ? "mt-5" : ""}>
            {/* ../loom's SidebarSection metrics: 20px above a label, 8px
                below. Its sections are Starred/Projects/Recents rather than
                days, but the day groups are the same thing — a named run of
                rows — and must read on the same rhythm. */}
            {g.label !== "Today" && <h3 className={"mt-5 mb-2 " + railLabel}>{g.label}</h3>}
            {/* No gap: the 28px row pitch is the rhythm, as ../loom has it. */}
            <ul className="flex flex-col">
              {g.items.map((t) => {
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
                        {/* The title runs out under a gradient to the row's
                            own background rather than ending in an ellipsis,
                            as ../loom's sidebar does. The text stays whole. */}
                        {t.title}
                        <span
                          aria-hidden="true"
                          className={
                            "pointer-events-none absolute inset-y-0 right-0 w-9 bg-gradient-to-r from-transparent " +
                            (active ? "to-rail-sel" : "to-panel group-hover:to-rail-hover")
                          }
                        />
                      </button>
                      {/* No dot on the row being written: a question is asked
                          at the top of the rail and its row is the one right
                          there, under the actions, so a marker beside it says
                          what the reader just did. The one case it would speak
                          for — a follow-up asked in an older thread, whose row
                          keeps its place down the list — is one the composer
                          already accounts for, and it is not worth a mark on
                          every other turn. */}
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
                          <Icon name="moreVertical" size="17px" />
                        </button>
                      )}
                    </div>
                    {menuOpen && (
                      <ThreadMenu
                        onRename={() => {
                          setOpenMenu(null);
                          setRenaming(t);
                        }}
                        onDelete={() => {
                          setOpenMenu(null);
                          setDeleting(t);
                        }}
                      />
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </div>
      {renaming && (
        <RenameThreadModal
          title={renaming.title}
          busy={pending}
          onCancel={() => setRenaming(null)}
          onSubmit={(title) => void rename(renaming, title)}
        />
      )}
      {deleting && (
        <DeleteThreadModal
          title={deleting.title}
          busy={pending}
          onCancel={() => setDeleting(null)}
          onDelete={() => void remove(deleting)}
        />
      )}
    </nav>
  );
}
