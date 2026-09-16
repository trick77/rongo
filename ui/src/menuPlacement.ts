import { useCallback, useLayoutEffect, useRef, useState } from "react";

/**
 * Finds the nearest scrollable ancestor, so "is there room below" is asked of
 * the element's own scroller rather than of the window.
 */
function nearestScrollParent(el: HTMLElement): HTMLElement | null {
  let parent = el.parentElement;
  while (parent !== null) {
    const overflowY = getComputedStyle(parent).overflowY;
    if (overflowY === "auto" || overflowY === "scroll") return parent;
    parent = parent.parentElement;
  }
  return null;
}

/**
 * Which way a popover opens, ../loom's hook. The thread rail clips its
 * overflow, so a menu opened on the last row would drop behind the end of
 * the list; the composer sits at the foot of the window, so a list opened
 * under it would leave the viewport. When there is no room below and more
 * above, the popover flips upward instead. The ref goes on the popover; it
 * hangs off its offsetParent, which must be the positioned anchor.
 */
export function useMenuPlacement(): {
  menuRef: React.RefObject<HTMLDivElement | null>;
  verticalClass: string;
} {
  const menuRef = useRef<HTMLDivElement>(null);
  const [dropUp, setDropUp] = useState(false);

  const measure = useCallback(() => {
    const el = menuRef.current;
    const anchor = el?.offsetParent as HTMLElement | null;
    if (el === null || anchor === null) return;
    const bounds = nearestScrollParent(el)?.getBoundingClientRect();
    const topLimit = Math.max(bounds?.top ?? 0, 0);
    const bottomLimit = Math.min(bounds?.bottom ?? window.innerHeight, window.innerHeight);
    const anchorRect = anchor.getBoundingClientRect();
    const menuHeight = el.offsetHeight + 4;
    const spaceBelow = bottomLimit - anchorRect.bottom;
    const spaceAbove = anchorRect.top - topLimit;
    setDropUp(spaceBelow < menuHeight && spaceAbove > spaceBelow);
  }, []);

  // Measured before paint so the popover never flashes downward first, and
  // kept right while it is open: scrolling moves the anchor it hangs off.
  useLayoutEffect(() => {
    measure();
    // Capture catches an ancestor's scroll, not only the window's.
    window.addEventListener("scroll", measure, true);
    window.addEventListener("resize", measure);
    return () => {
      window.removeEventListener("scroll", measure, true);
      window.removeEventListener("resize", measure);
    };
  }, [measure]);

  return { menuRef, verticalClass: dropUp ? "top-auto bottom-full mb-1" : "top-full mt-1" };
}
