import { useCallback, useLayoutEffect, useRef, useState } from "react";

import { Icon, type IconName } from "./Icon";

/**
 * The row actions menu, ../loom's: an inset entry whose hover ground floats
 * inside the menu rather than running to its edges. The grey fill is baked
 * into the class so no entry can be added without it.
 */
const entry =
  "mx-1 flex min-h-[30px] w-[calc(100%-0.5rem)] items-center gap-2.5 rounded-ui-sm px-3 py-1 text-left text-sm/5 transition-colors";
const plainEntry = entry + " text-ink hover:bg-rail-hover";
/**
 * Delete is muted red at rest and a solid red fill on hover — the one entry
 * that cannot be taken back, and the only place the danger token is a ground
 * rather than a word.
 */
const dangerEntry = entry + " text-danger hover:bg-danger hover:text-ink";

/**
 * Finds the nearest scrollable ancestor, so "is there room below" is asked of
 * the rail's own scroller rather than of the window.
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
 * Which way the menu opens, ../loom's hook. The rail clips its overflow, so a
 * menu opened on the last row would drop behind the end of the list. When
 * there is no room below and more above, it flips upward instead.
 */
function useMenuPlacement(): {
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

  // Measured before paint so the menu never flashes downward first, and kept
  // right while it is open: scrolling the rail moves the row it hangs off.
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

function MenuIcon({ name }: { name: IconName }) {
  return (
    <span aria-hidden="true" className="grid h-[21px] w-[21px] shrink-0 place-items-center">
      <Icon name={name} size="17px" />
    </span>
  );
}

/**
 * What a thread row can be told to do. Anchored to the row's right edge, not
 * pushed off the title as ../loom does it: the rail is 362px wide and a menu
 * offset past the title would run out of its scroller sideways.
 */
export default function ThreadMenu({
  onRename,
  onDelete,
}: {
  onRename: () => void;
  onDelete: () => void;
}) {
  const { menuRef, verticalClass } = useMenuPlacement();
  return (
    <div
      ref={menuRef}
      role="menu"
      aria-label="Thread actions"
      className={
        "absolute right-1 left-auto z-20 w-[168px] overflow-hidden rounded-ui border border-elevated-border " +
        "bg-elevated py-1 shadow-panel " +
        verticalClass
      }
    >
      <button type="button" role="menuitem" className={plainEntry} onClick={onRename}>
        <MenuIcon name="edit" />
        Rename
      </button>
      {/* The separator is the whole point of the order: Delete must not sit
          one slip of the finger away from Rename. */}
      <div role="separator" className="mx-[14px] my-[5px] h-px bg-elevated-border" />
      <button type="button" role="menuitem" className={dangerEntry} onClick={onDelete}>
        <MenuIcon name="trash" />
        Delete
      </button>
    </div>
  );
}
