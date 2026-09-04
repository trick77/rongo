import { type ReactNode, useEffect, useId, useRef, useState } from "react";

/**
 * The dialogs the rail's row menu opens, ../loom's two. Both are modal: a
 * rename that is half typed and a delete that cannot be taken back are the
 * only things on screen while they are up.
 */

const cancelButton =
  "h-8 rounded-ui-sm px-3.5 text-sm font-medium text-ink-dim transition-colors hover:bg-elevated";
const dangerButton =
  "h-8 rounded-ui-sm bg-danger px-3.5 text-sm font-medium text-ink transition-colors hover:brightness-110 disabled:opacity-50";
const saveButton =
  "h-8 rounded-ui-sm bg-accent-fill px-3.5 text-sm font-medium text-ink transition-colors hover:bg-accent-strong disabled:opacity-50";

export function ModalShell({
  title,
  children,
  onCancel,
}: {
  title: string;
  children: ReactNode;
  onCancel: () => void;
}) {
  const titleID = useId();
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") onCancel();
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onCancel]);
  return (
    <div
      className="fixed inset-0 z-40 grid place-items-center bg-black/50 px-4 backdrop-blur-[2px]"
      // Only the backdrop itself dismisses: a click that started inside the
      // dialog and ended on the backdrop is a drag, not a cancel.
      onClick={(e) => {
        if (e.target === e.currentTarget) onCancel();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleID}
        className="w-full max-w-[390px] rounded-ui-lg border border-elevated-border bg-active p-[18px] shadow-panel"
      >
        <h2 id={titleID} className="text-[19px]/7 font-semibold text-ink">
          {title}
        </h2>
        {children}
      </div>
    </div>
  );
}

export function RenameThreadModal({
  title,
  busy,
  onCancel,
  onSubmit,
}: {
  title: string;
  busy: boolean;
  onCancel: () => void;
  onSubmit: (title: string) => void;
}) {
  const [value, setValue] = useState(title);
  const input = useRef<HTMLInputElement | null>(null);
  // Selected, not just focused: the title is already there, and renaming
  // usually means replacing it rather than editing a word of it.
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);
  return (
    <ModalShell title="Rename thread" onCancel={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit(value.trim());
        }}
      >
        <input
          ref={input}
          aria-label="Thread title"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          className="mt-3 h-[38px] w-full rounded-ui-sm border border-border bg-bg px-3 text-ink outline-none focus:border-accent"
        />
        <div className="mt-4 flex justify-end gap-2">
          <button type="button" className={cancelButton} onClick={onCancel}>
            Cancel
          </button>
          <button type="submit" className={saveButton} disabled={busy || value.trim() === ""}>
            Save
          </button>
        </div>
      </form>
    </ModalShell>
  );
}

export function DeleteThreadModal({
  title,
  busy,
  onCancel,
  onDelete,
}: {
  title: string;
  busy: boolean;
  onCancel: () => void;
  onDelete: () => void;
}) {
  return (
    <ModalShell title="Delete thread" onCancel={onCancel}>
      <p className="mt-3 text-sm/6 text-ink-dim">
        “{title}” and every question and answer in it are deleted. This cannot be undone.
      </p>
      {/* Cancel takes the focus, not Delete: the safe half of an irreversible
          choice is the one Enter should land on. */}
      <div className="mt-4 flex justify-end gap-2">
        <button autoFocus type="button" className={cancelButton} onClick={onCancel}>
          Cancel
        </button>
        <button type="button" className={dangerButton} disabled={busy} onClick={onDelete}>
          Delete
        </button>
      </div>
    </ModalShell>
  );
}
