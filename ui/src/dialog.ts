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
