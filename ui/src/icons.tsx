/**
 * The two glyphs the shell draws itself, as inline SVG. Everything else comes
 * from the icon font (Icon.tsx); these two stay hand-drawn because the chevron
 * has to rotate and the plus is the one ../loom also keeps as SVG.
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
