import { useEffect, useState } from "react";

import { Icon } from "./Icon";
import ThreadMenu from "./ThreadMenu";
import { DeleteThreadModal, RenameThreadModal } from "./ThreadModals";

/**
 * The rail's label size, ../loom's: 12/16 in sentence case, not an uppercase
 * tracked eyebrow. Exported because the "History" umbrella in App is the same
 * label as the day groups below it and must not drift from them.
 */
export const railLabel = "px-1.5 text-xs/4 text-rail-label";

export type Thread = {
  id: number;
  title: string;
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
  onList = () => {},
  onDeleted = () => {},
  onRenamed = () => {},
}: {
  activeId: number | null;
  /** Only ever a real thread: clearing to a new question is the rail's job. */
  onSelect: (id: number) => void;
  version: number;
  busy?: boolean;
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
    // The 28px pitch is the desktop rhythm; a finger needs more. Keyed off the
    // pointer, not the width, so an iPad gets the bigger row too — and an iPad
    // on a trackpad reports a fine pointer and keeps the tight one.
    //
    // No disabled: dimming — this class dresses the row's <div>, which cannot
    // be disabled. The title button inside it carries its own.
    "flex h-7 pointer-coarse:h-11 w-full items-center gap-2 rounded-md pr-1 pl-1.5 text-left text-sm/5";

  return (
    <nav aria-label="Threads" className="flex min-h-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 overflow-auto px-2 pb-4">
        {groups.map((g) => (
          // "Today" is not painted: it always heads the list, directly under
          // the "History" label, and would only name the same thing twice.
          // The group still exists, so tomorrow's threads split off it.
          //
          // Unpainted, it owes "History" the 8px a label owes its rows, so it
          // carries mt-2 itself. A painted group owes the 20px a label gets
          // instead, which its own h3 carries — that is what keeps a morning
          // with nothing asked yet, where "Yesterday" is the first thing under
          // "History", reading as two labels rather than a label and a gap.
          <div key={g.label} className={g.label === "Today" ? "mt-2" : ""}>
            {/* ../loom's SidebarSection metrics: 20px above a label, 8px
                below. Its sections are Starred/Projects/Recents rather than
                days, but the day groups are the same thing — a named run of
                rows — and must read on the same rhythm. */}
            {g.label !== "Today" && <h3 className={"mt-5 mb-2 " + railLabel}>{g.label}</h3>}
            {/* No gap: the 28px row pitch is the rhythm, as ../loom has it. */}
            <ul className="flex flex-col">
              {g.items.map((t) => {
                const active = t.id === activeId;
                // Gated on busy as well as on the id: the trigger is dropped
                // the moment a turn starts, and a menu left standing over it
                // would still offer Delete on the thread being written.
                const menuOpen = openMenu === t.id && !busy;
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
                        // Switching away from a running turn is what busy
                        // locks out. The running thread's own row is not a
                        // switch: it is the way back from the Repos page
                        // while the answer is still being written, and with
                        // the page nav gone it is the only one.
                        disabled={busy && !active}
                        className="relative min-w-0 flex-1 overflow-hidden text-left whitespace-nowrap disabled:opacity-50"
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
                      {active && busy && (
                        <span aria-hidden="true" className="pulse h-1.5 w-1.5 shrink-0 self-center rounded-full bg-accent-strong" />
                      )}
                      {/* No actions at all while a turn is running: deleting
                          the thread being written would pull the record out
                          from under the answer still landing on it. */}
                      {!busy && (
                        <button
                          type="button"
                          aria-haspopup="menu"
                          aria-expanded={menuOpen}
                          aria-label={"Actions for " + t.title}
                          onClick={() => setOpenMenu(menuOpen ? null : t.id)}
                          // Quiet on an idle row, but never unreachable: it
                          // comes back for the keyboard and on touch, where
                          // there is no hover to reveal it.
                          className={
                            "grid h-6 w-6 shrink-0 place-items-center rounded-md text-rail transition-colors hover:bg-active hover:text-ink " +
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
