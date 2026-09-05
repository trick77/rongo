/**
 * The glyphs the shell draws itself, as inline SVG. Everything else comes from
 * the icon font (Icon.tsx); these stay hand-drawn because the chevron has to
 * rotate, the plus is the one ../loom also keeps as SVG, and the two diagram
 * controls have no codepoint in the font — Icon.tsx's map is copied from
 * ../loom, never guessed at.
 */

/**
 * Chevron, rotating 90 degrees on open. No triangle, no plus/minus, and the
 * same glyph in both states — only the rotation changes (AGENTS.md).
 */
export function Chevron({ open = false }: { open?: boolean }) {
  return (
    <svg
      className={"chev inline-block h-3 w-3 transition-transform " + (open ? "rotate-90" : "")}
      viewBox="0 0 12 12"
      aria-hidden="true"
    >
      <path
        d="M4.5 2.5 L8 6 L4.5 9.5"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

/** The plus inside the new-question button's disc. 13px, as ../loom draws it. */
export function PlusIcon() {
  return (
    <svg
      className="h-[13px] w-[13px] shrink-0"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      aria-hidden="true"
    >
      <path d="M8 3v10M3 8h10" strokeLinecap="round" />
    </svg>
  );
}

/** Corners pointing out: the diagram's full view. Not a chevron — nothing is
 * being disclosed here, the same picture opens in a bigger box. */
export function ExpandIcon() {
  return (
    <svg
      className="h-[15px] w-[15px] shrink-0"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M6 2H2v4M10 2h4v4M6 14H2v-4M10 14h4v-4" />
    </svg>
  );
}

/** An arrow into a tray: the diagram as a file. */
export function DownloadIcon() {
  return (
    <svg
      className="h-[15px] w-[15px] shrink-0"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M8 2v8M5 7.5 8 10.5 11 7.5M2.5 13h11" />
    </svg>
  );
}

/** Two sheets, one behind the other: the question on the clipboard. */
export function CopyIcon() {
  return (
    <svg
      className="h-[14px] w-[14px] shrink-0"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <rect x="5.5" y="5.5" width="8" height="8" rx="1.5" />
      <path d="M10.5 3.5A1.5 1.5 0 0 0 9 2H4a1.5 1.5 0 0 0-1.5 1.5v5A1.5 1.5 0 0 0 4 10" />
    </svg>
  );
}

/**
 * The copy landed. Takes the copy glyph's place for a moment: the answer's
 * own copy button swaps its label the same way, and on a page where nothing
 * else moved a click with no feedback reads as a click that missed.
 */
export function CheckIcon() {
  return (
    <svg
      className="h-[14px] w-[14px] shrink-0"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3.5 8.5 6.5 11.5 12.5 4.5" />
    </svg>
  );
}
