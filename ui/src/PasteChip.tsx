import { useState } from "react";
import { Chevron } from "./icons";

/**
 * One pasted block, folded to a line: "Pasted text · 12 lines" with a
 * chevron, the text itself under it on a click. The same chip stands in the
 * composer, where it carries a × to take the paste back, and at the head of a
 * sent turn, where it does not.
 *
 * Folded by default in both places. The paste is the reader's own material
 * and they know what is in it; what they want on screen is the answer, not a
 * screen of their own stack trace above it. Open, the text is a <pre> capped
 * in height with its own scroll, so a long log never pushes the turn away.
 */
export default function PasteChip({ text, lines, onRemove }: { text: string; lines: number; onRemove?: () => void }) {
  const [open, setOpen] = useState(false);
  const label = `Pasted text · ${lines} ${lines === 1 ? "line" : "lines"}`;

  return (
    <div className="paste-chip">
      <div className="flex items-center">
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
          className="flex min-w-0 items-center gap-1.5 py-1 pr-1 pl-2 text-left text-[12.5px] text-ink-dim hover:text-ink"
        >
          <Chevron open={open} />
          <span className="truncate">{label}</span>
        </button>
        {onRemove && (
          <button
            type="button"
            onClick={onRemove}
            aria-label="Remove pasted text"
            className="grid h-7 w-7 place-items-center rounded-ui-sm text-base leading-none text-muted hover:bg-active hover:text-ink"
          >
            ×
          </button>
        )}
      </div>
      {open && <pre className="paste-chip-pre">{text}</pre>}
    </div>
  );
}
