import { type ReactNode } from "react";
import { Chevron } from "./icons";

/**
 * DecisionCard is the card a turn asks back with — which one was meant, or
 * which repositories — folded under one line. Ochre means "your move": the
 * card wears it only while a reader who can act on it has not. The chevron
 * rotates toward what it opened.
 */
export default function DecisionCard({
  ochre,
  open,
  onToggle,
  title,
  children,
}: {
  ochre: boolean;
  open: boolean;
  onToggle: () => void;
  /** The one line that stays when the card is folded. */
  title: ReactNode;
  /** The body, built only while open: a folded card with many candidates
   * is not worth rendering on every streamed token. */
  children: () => ReactNode;
}) {
  return (
    <div className={"mt-4 rounded-ui border bg-panel " + (ochre ? "border-ochre" : "border-border")}>
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="flex w-full items-center gap-3 px-4 py-3 text-left text-[14.5px]"
      >
        <Chevron open={open} />
        {title}
      </button>

      {open && children()}
    </div>
  );
}
