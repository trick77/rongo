import { useEffect } from "react";

/**
 * Closes an open menu on a pointer anywhere but the menu itself and its
 * own trigger — another row's title included, which switches thread and
 * would otherwise leave the menu hanging off the row that was left — and on
 * Escape. The open trigger is spared because its own click toggles, and
 * closing here first would make the toggle reopen the menu that was just
 * dismissed. Only the OPEN one: the header's chevron and the rail's kebabs
 * are on screen together, and a pointer on another trigger must close this
 * menu before that trigger opens its own, or two stand open at once. Bound
 * only while a menu is open: the rail, the Threads page and the header each
 * keep their own open state and share this one listener.
 */
export function useMenuDismiss(open: boolean, close: () => void) {
  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: PointerEvent) {
      const target = e.target;
      if (target instanceof Element && target.closest('[role="menu"], [aria-haspopup="menu"][aria-expanded="true"]')) return;
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
