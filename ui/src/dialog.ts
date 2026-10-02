import { useEffect, type RefObject } from "react";

/** useEscape closes on Escape. On the window rather than the document: an
 * event dispatched on either reaches it, and every dialog closes the same
 * way. */
export function useEscape(onClose: () => void): void {
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
}

/** useFocusOnOpen moves focus into the dialog on open and back to where it
 * was on close, so a keyboard reader does not land at the top of the page
 * afterwards. */
export function useFocusOnOpen(first: RefObject<HTMLElement | null>): void {
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    first.current?.focus();
    return () => before?.focus?.();
    // Once, on mount: the dialog's first control does not change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
}

/** What Tab stops on inside a dialog. A scrolling body is in the ring only
 * when it says so with tabindex="0": the browser's own keyboard-focusable
 * scroller cannot be asked for, and a trap that did not know about it would
 * wrap past it and leave a long body unreachable by keyboard. */
const TABBABLE =
  'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

/** useTabTrap keeps Tab inside a modal dialog: past its last stop the ring
 * wraps to the first, and back. Without it Tab walks into the dimmed page
 * behind the scrim — the citation chips of the answer are focusable — and
 * Enter there opens a second overlay under this one. Between the ends the
 * browser moves the focus itself. */
export function useTabTrap(dialog: RefObject<HTMLElement | null>): void {
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key !== "Tab") return;
      const inside = dialog.current?.querySelectorAll<HTMLElement>(TABBABLE);
      if (!inside || inside.length === 0) return;
      const first = inside[0];
      const last = inside[inside.length - 1];
      const on = document.activeElement;
      if (!e.shiftKey && (on === last || !dialog.current?.contains(on))) {
        e.preventDefault();
        first.focus();
      } else if (e.shiftKey && (on === first || !dialog.current?.contains(on))) {
        e.preventDefault();
        last.focus();
      }
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [dialog]);
}
