import { Icon, type IconName } from "./Icon";
import { railRow } from "./rail";

/**
 * RailLink is one place on the rail: Threads, Shared, Projects, Memory, and
 * the "All threads" foot. Marked current when the reader is there, except a
 * door like the foot, which is never a place. The icon sits in the same 20px
 * slot as the plus disc on "New question": the Icon glyph is text, so its box
 * is whatever advance width the font gives it — 21px here — and without the
 * slot the labels would start a pixel apart.
 */
export function RailLink({
  icon,
  label,
  current = false,
  extra = "",
  onClick,
}: {
  icon: IconName;
  label: string;
  current?: boolean;
  /** Classes the one site adds, such as the foot's top margin. */
  extra?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-current={current ? "page" : undefined}
      onClick={onClick}
      className={railRow + " " + (current ? "bg-rail-sel text-white" : "text-rail hover:bg-rail-hover") + (extra ? " " + extra : "")}
    >
      <span className="grid h-5 w-5 shrink-0 place-items-center">
        <Icon name={icon} size="21px" className="text-ink-dim" />
      </span>
      {label}
    </button>
  );
}
