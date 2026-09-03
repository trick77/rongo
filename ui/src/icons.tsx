/**
 * The handful of glyphs the shell needs, as inline SVG. Three icons are not
 * worth a dependency, and inline SVG inherits currentColor for free.
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

const glyph = "h-4 w-4 shrink-0";

export function AskIcon() {
  return (
    <svg className={glyph} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
      <path d="M2 3h12v8H6l-3 3v-3H2z" strokeLinejoin="round" />
    </svg>
  );
}

export function ReposIcon() {
  return (
    <svg className={glyph} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
      <circle cx="5" cy="4" r="1.5" />
      <circle cx="5" cy="12" r="1.5" />
      <circle cx="11" cy="6" r="1.5" />
      <path d="M5 5.5v5M11 7.5c0 2-6 1-6 3" />
    </svg>
  );
}

export function PlusIcon() {
  return (
    <svg className={glyph} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
      <path d="M8 3v10M3 8h10" strokeLinecap="round" />
    </svg>
  );
}
