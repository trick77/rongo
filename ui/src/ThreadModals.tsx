import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";

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
  // Mounted on the body, not where it is written. The rail these dialogs
  // open from is a drawer that slides, and a translated ancestor is the
  // containing block for everything fixed inside it — `inset-0` resolved to
  // the 362px rail, so the scrim, the blur and the centering all stopped at
  // its border. ../loom escapes the same transform by rendering its modals
  // from the thread shell instead; a portal is that, without moving the
  // state out of the component that owns it.
  return createPortal(
    <div
      // Above the rail's own z-50, not the z-40 an in-rail dialog could get
      // away with: on a phone the dialog is opened from the drawer, which is
      // still standing when it appears, and a body-mounted z-40 paints under
      // it.
      //
      // Centered in the window, like SourceView's and DiagramView's overlays.
      // The card used to be offset by the rail so it would center in the
      // content area instead, but there is no stable center there to aim at:
      // Ask hangs a 300px sources aside on the right from xl, 340 from 2xl,
      // and the Repos page has none — the offset was right at one width of
      // one page and visibly off everywhere else.
      className="fixed inset-0 z-[60] grid place-items-center bg-black/50 px-4 backdrop-blur-[2px]"
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
    </div>,
    document.body,
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
