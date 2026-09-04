import type { CSSProperties } from "react";

/**
 * A glyph from the "Anthropic Icons" variable font (the @font-face is in
 * index.css, the file itself is the one ../loom ships).
 *
 * The codepoints sit in the private-use area U+E000–U+E11E and the font
 * carries no speaking names for them, so the map below is the name. It is a
 * subset of ../loom's — carry a codepoint over from there when a glyph is
 * needed, never guess one.
 *
 * Sizing is a font-size, not a class, because the glyph is text: the box is
 * whatever the size makes it.
 *
 *   <Icon name="code" size="21px" />
 *   <Icon name="code" size="21px" label="Repositories" />
 */
const CODEPOINTS = {
  code: 0xe048,
} as const;

export type IconName = keyof typeof CODEPOINTS;

/** Name → glyph string, for a CSS `content` or a test. */
export const ICONS = Object.fromEntries(
  Object.entries(CODEPOINTS).map(([name, cp]) => [name, String.fromCodePoint(cp)]),
) as Record<IconName, string>;

export function Icon({
  name,
  className = "",
  size = "1.33rem",
  label,
}: {
  name: IconName;
  className?: string;
  /** The glyph's font-size, which is what controls its size. */
  size?: string;
  /**
   * Set only when the icon carries meaning on its own. Beside a label it is
   * decoration, and a screen reader should skip it rather than read it twice.
   */
  label?: string;
}) {
  const style: CSSProperties = {
    fontFamily: '"Anthropic Icons"',
    fontSize: size,
    lineHeight: 1,
    fontStyle: "normal",
    fontWeight: 400,
    display: "inline-block",
    flexShrink: 0,
  };
  return (
    <span
      className={className}
      style={style}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      {String.fromCodePoint(CODEPOINTS[name])}
    </span>
  );
}
