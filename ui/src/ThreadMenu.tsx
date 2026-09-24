import { Icon, type IconName } from "./Icon";
import type { Thread } from "./Threads";
import { useMenuPlacement } from "./menuPlacement";

/**
 * The row actions menu, ../loom's: an inset entry whose hover ground floats
 * inside the menu rather than running to its edges, and lights up under the
 * pointer rather than dimming. The fill is baked into the class so no entry
 * can be added without it; `enabled:` keeps a disabled entry flat, which
 * only matches on a <button>, so entries stay buttons.
 */
const entry =
  "mx-1 flex min-h-[30px] w-[calc(100%-0.5rem)] items-start gap-2.5 rounded-md px-3 py-1 text-left text-sm/5 transition-colors";
const plainEntry = entry + " text-elevated-ink enabled:hover:bg-elevated-hover";
/**
 * Delete is a light red word at rest and a solid red fill on hover — the one
 * entry that cannot be taken back, and the only place danger is a ground
 * rather than a word.
 */
const dangerEntry = entry + " text-danger-ink enabled:hover:bg-danger-fill enabled:hover:text-white";

function MenuIcon({ name }: { name: IconName }) {
  return (
    <span aria-hidden="true" className="grid h-[21px] w-[21px] shrink-0 place-items-center text-[19px] leading-none">
      <Icon name={name} size="19px" />
    </span>
  );
}

/**
 * What a thread row can be told to do. Anchored to the row's right edge, not
 * pushed off the title as ../loom does it: the rail is 362px wide and a menu
 * offset past the title would run out of its scroller sideways. `className`
 * moves the anchor for the one place that is not a row, the header's chevron.
 */
/** The four things a menu can start on a thread, as useThreadActions returns
 * them. */
export type ThreadActions = {
  startStar: (t: Thread) => void;
  startShare: (t: Thread) => void;
  startRename: (t: Thread) => void;
  startDelete: (t: Thread) => void;
};

/** ThreadMenuFor is the menu wired to one thread: every entry closes the
 * menu, then starts its action on that thread. The rail, the Threads page
 * and the header each drew these four closures by hand. */
export function ThreadMenuFor({
  thread,
  actions,
  onPick,
  className,
}: {
  thread: Thread;
  actions: ThreadActions;
  onPick: () => void;
  className?: string;
}) {
  const pick = (start: (t: Thread) => void) => () => {
    onPick();
    start(thread);
  };
  return (
    <ThreadMenu
      className={className}
      starred={thread.starred}
      onStar={pick(actions.startStar)}
      onShare={pick(actions.startShare)}
      onRename={pick(actions.startRename)}
      onDelete={pick(actions.startDelete)}
    />
  );
}

export default function ThreadMenu({
  starred = false,
  onStar,
  onShare,
  onRename,
  onDelete,
  className = "right-1 left-auto",
}: {
  /** Whether the first entry reads Unstar rather than Star. */
  starred?: boolean;
  onStar: () => void;
  onShare: () => void;
  onRename: () => void;
  onDelete: () => void;
  className?: string;
}) {
  const { menuRef, verticalClass } = useMenuPlacement();
  return (
    <div
      ref={menuRef}
      role="menu"
      aria-label="Thread actions"
      className={
        "absolute z-20 w-[168px] overflow-hidden rounded-ui border border-elevated-border " +
        "bg-elevated py-1 shadow-menu " +
        className +
        " " +
        verticalClass
      }
    >
      {/* Star first, ../loom's order: the one entry that changes nothing
          about the record, only where the rail files it. */}
      <button type="button" role="menuitem" className={plainEntry} onClick={onStar}>
        <MenuIcon name={starred ? "starOff" : "star"} />
        {starred ? "Unstar" : "Star"}
      </button>
      <button type="button" role="menuitem" className={plainEntry} onClick={onShare}>
        <MenuIcon name="upload" />
        Share
      </button>
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
