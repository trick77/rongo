import { useEffect } from "react";

/**
 * Closes an open menu on a pointer anywhere but the menu itself and its
 * triggers — another row's title included, which switches thread and would
 * otherwise leave the menu hanging off the row that was left — and on
 * Escape. The triggers are spared because their own click toggles, and
 * closing here first would make the toggle reopen the menu that was just
 * dismissed. Bound only while a menu is open: the rail, the Threads page and
 * the header each keep their own open state and share this one listener.
 */
export function useMenuDismiss(open: boolean, close: () => void) {
  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: PointerEvent) {
      const target = e.target;
      if (target instanceof Element && target.closest('[role="menu"], [aria-haspopup="menu"]')) return;
      close();
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") close();
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
}
