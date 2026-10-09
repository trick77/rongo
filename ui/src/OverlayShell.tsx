import { useRef, type ReactNode } from "react";
import { useEscape, useFocusOnOpen, useTabTrap } from "./dialog";
import { useBackdropDismiss } from "./dismiss";

/** The header the source and commit viewers share; the diagram passes its own
 * gap. */
export const overlayHeader = "flex items-center gap-2 border-b border-border px-3 py-2.5 sm:gap-3.5 sm:px-4.5 sm:py-3";

/** The close button: a 44px target on a phone, 32px from sm up. */
export const overlayClose =
  "grid h-11 w-11 place-items-center rounded-ui-sm text-lg leading-none text-muted hover:bg-active hover:text-ink sm:h-8 sm:w-8";

/**
 * OverlayShell is the one overlay the app draws: a file, a commit or a
 * diagram seen whole. The reader is stepping out of the answer for a moment
 * and goes straight back, and one overlay at a time is what the app does, so
 * the three share the scrim, the sheet, the header with its × and the
 * keyboard: focus moves in on open and back on close, Escape closes, and Tab
 * stays inside — the dialog is modal, and a Tab that left it would land in
 * the dimmed page behind.
 *
 * Edge to edge on a phone: 24px of scrim on each side buys nothing when the
 * code inside is already scrolling sideways. A press beside the sheet closes
 * it and does nothing else: see useBackdropDismiss for why that is the click
 * and not the pointerdown.
 */
export default function OverlayShell({
  label,
  dialogClassName,
  headerClassName = overlayHeader,
  closeClassName = overlayClose,
  header,
  onClose,
  children,
}: {
  label: string;
  /** The sheet's own grid and width; the three differ. */
  dialogClassName: string;
  headerClassName?: string;
  closeClassName?: string;
  /** What sits in the header before the × . */
  header: ReactNode;
  onClose: () => void;
  children: ReactNode;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  const dismiss = useBackdropDismiss(onClose);

  useFocusOnOpen(closeButton);
  useEscape(onClose);
  useTabTrap(dialog);

  return (
    <div
      className="fixed inset-0 z-30 flex items-center justify-center bg-black/55 p-0 sm:p-6 md:p-10"
      ref={dismiss.ref}
      onPointerDown={dismiss.onPointerDown}
      onPointerUp={dismiss.onPointerUp}
    >
      <div ref={dialog} role="dialog" aria-modal="true" aria-label={label} className={dialogClassName}>
        <header className={headerClassName}>
          {header}
          <button ref={closeButton} type="button" onClick={onClose} aria-label="Close" className={closeClassName}>
            ×
          </button>
        </header>
        {children}
      </div>
    </div>
  );
}
