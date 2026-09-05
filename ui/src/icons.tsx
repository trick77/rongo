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
