import { useState, type ReactNode } from "react";

import type { Thread } from "./Threads";
import { DeleteThreadModal, RenameThreadModal } from "./ThreadModals";
import ShareDialog from "./share/ShareDialog";
import { shareFor, type Share } from "./share/api";

/**
 * The actions a thread row offers — star, share, rename, delete — and the
 * dialogs they open. The rail and the Threads page both list threads with the
 * same kebab, and this is the one place the requests and their dialogs live;
 * each list keeps its own rows and hears back through the callbacks.
 *
 * Both actions answer 204 and carry nothing back, so the caller drops the row
 * or rewrites the title from what was asked for rather than from a response
 * body. A failure leaves the dialog up: the row is still there, and telling
 * someone their thread is gone when it is not is worse than saying nothing.
 */
export function useThreadActions({
  onRenamed,
  onDeleted,
  onShared,
  onStarred,
}: {
  onRenamed: (id: string, title: string) => void;
  onDeleted: (id: string) => void;
  /** A link was made or taken back; `shared` is what the row should show. */
  onShared: (id: string, shared: boolean) => void;
  /** The star was put on or taken off; `starred` is what the row should show. */
  onStarred: (id: string, starred: boolean) => void;
}) {
  // Which thread a dialog is asking about. Objects rather than ids only for
  // the title the dialog shows; the list may reload underneath them.
  const [renaming, setRenaming] = useState<Thread | null>(null);
  const [deleting, setDeleting] = useState<Thread | null>(null);
  // The thread whose link is being handed out, and the link it already has.
  // Fetched before the dialog opens, so it never flashes "Share thread" at
  // someone whose thread is already shared.
  const [sharing, setSharing] = useState<{ thread: Thread; share: Share | null } | null>(null);
  const [pending, setPending] = useState(false);

  /**
   * No dialog: a star is one click and one click takes it back. 204 like
   * the rest, so the row is patched from what was asked for. A failure is
   * silent for the same reason a failed rename is: the row still shows what
   * is stored.
   */
  async function star(t: Thread) {
    const starred = !t.starred;
    try {
      const res = await fetch(`/api/threads/${t.id}/${starred ? "star" : "unstar"}`, { method: "POST" });
      if (!res.ok) return;
      onStarred(t.id, starred);
    } catch {
      // The row keeps the star it had.
    }
  }

  async function rename(t: Thread, title: string) {
    setPending(true);
    try {
      const res = await fetch(`/api/threads/${t.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title }),
      });
      if (!res.ok) return;
      setRenaming(null);
      onRenamed(t.id, title);
    } catch {
      // Nothing to say: the title on screen is still the stored one.
    } finally {
      setPending(false);
    }
  }

  /**
   * Opens the dialog on what the thread already has. The link is asked for
   * first rather than the dialog fetching for itself: a dialog that opened on
   * "not shared" and corrected itself a beat later would offer Create on a
   * thread that is already out there.
   */
  async function share(t: Thread) {
    let share: Share | null = null;
    try {
      share = await shareFor(t.id);
    } catch {
      // Treated as "no link yet": Create then answers with the link the thread
      // already has, because the server keeps the token it minted.
    }
    setSharing({ thread: t, share });
  }

  async function remove(t: Thread) {
    setPending(true);
    try {
      const res = await fetch(`/api/threads/${t.id}`, { method: "DELETE" });
      if (!res.ok) return;
      setDeleting(null);
      onDeleted(t.id);
    } catch {
      // Same: the row stays, and the dialog with it.
    } finally {
      setPending(false);
    }
  }

  const dialogs: ReactNode = (
    <>
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
      {sharing && (
        <ShareDialog
          threadID={sharing.thread.id}
          title={sharing.thread.title}
          share={sharing.share}
          onCancel={() => setSharing(null)}
          onChange={(share) => {
            setSharing((prev) => (prev ? { ...prev, share } : prev));
            onShared(sharing.thread.id, share !== null);
          }}
        />
      )}
    </>
  );

  return {
    startStar: (t: Thread) => void star(t),
    startShare: (t: Thread) => void share(t),
    startRename: setRenaming,
    startDelete: setDeleting,
    /** The dialogs, to render once wherever the list is. */
    dialogs,
  };
}
