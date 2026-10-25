/**
 * The look of rongo's diagrams, which is not the chrome's: the dark palette,
 * the IBM Plex pair and the type sizes of an engineering-memo reference
 * design (index.css, the --diagram-* tokens). Plus the
 * text measure the layout sizes boxes by.
 *
 * Roles, not values: the drawing asks for `fg` or `line`, and index.css says
 * what that is.
 */

export type Palette = {
  /** The canvas and a box's fill. */
  surface: string;
  fg: string;
  /** Arrows, arrow labels, secondary text. */
  muted: string;
  /** Every frame. */
  line: string;
  /** An outcome's fill and a zone's wash. */
  tint: string;
  /** The focus: what the question is about, tagged `:::focus`. */
  accent: string;
  /** A boundary: a dotted arrow in the source, async or optional. */
  warn: string;
  sans: string;
  mono: string;
};

/** Read off the document like themeVariables() in diagram.tsx: the drawing
 * is a string of SVG, and a downloaded file has no stylesheet to resolve
 * var() against, so every value goes in resolved. */
const roles: Record<keyof Palette, string> = {
  surface: "--diagram-surface",
  fg: "--diagram-fg",
  muted: "--diagram-muted",
  line: "--diagram-line",
  tint: "--diagram-tint",
  accent: "--diagram-accent",
  warn: "--diagram-warn",
  sans: "--diagram-sans",
  mono: "--diagram-mono",
};

/** palette is the roles resolved. A token the document lacks (jsdom loads
 * no stylesheet) stays a var() reference, which still draws on the page. */
export function palette(): Palette {
  const style =
    typeof getComputedStyle === "function"
      ? getComputedStyle(document.documentElement)
      : null;
  const out = {} as Palette;
  for (const [role, token] of Object.entries(roles) as [
    keyof Palette,
    string,
  ][]) {
    const v = style?.getPropertyValue(token).trim() ?? "";
    out[role] = v !== "" ? v : `var(${token})`;
  }
  return out;
}

/** The type ramp, in px, the reference's own: a node in the sans, a start or an
 * end in its bold, a name from the code in the mono, an arrow label in the
 * smaller mono. Drawn 1:1; a wide diagram scrolls rather than shrinks. */
export const ramp = {
  text: 13,
  bold: 13.5,
  code: 12,
  label: 11.5,
  lineHeight: 18,
} as const;

export type Face = "sans" | "mono";

/** Advance per character, in em, measured in Chrome over corpus labels
 * (docs/measurements/2026-10-25-diagram-text-width.md): the mono is exactly
 * 0.6, IBM Plex Sans 0.46 to 0.50 at 400 and up to 0.52 at 600, budgeted at
 * 0.56 so a box is never short for its label. At 0.6 every box wrapped a
 * line early. A budget rather than a measurement on the page because it is
 * the same under jsdom, where text has no width, and before the web font
 * has loaded, so a layout is a function of its text alone. */
const advance: Record<Face, number> = { sans: 0.56, mono: 0.6 };

/** textWidth is the width a string takes at a size: a wide or full-width
 * character costs 1em, a mark nothing, any other character its face's
 * advance. */
export function textWidth(s: string, size: number, face: Face): number {
  let em = 0;
  for (const ch of s) {
    if (/\p{M}/u.test(ch)) continue;
    em += wide(ch) ? 1 : advance[face];
  }
  return em * size;
}

function wide(ch: string): boolean {
  const c = ch.codePointAt(0)!;
  return (
    (c >= 0x1100 && c <= 0x115f) ||
    (c >= 0x2e80 && c <= 0xa4cf) ||
    (c >= 0xac00 && c <= 0xd7a3) ||
    (c >= 0xf900 && c <= 0xfaff) ||
    (c >= 0xfe30 && c <= 0xfe4f) ||
    (c >= 0xff00 && c <= 0xff60) ||
    (c >= 0xffe0 && c <= 0xffe6) ||
    (c >= 0x1f300 && c <= 0x1faff) ||
    (c >= 0x20000 && c <= 0x3fffd)
  );
}

/** wrap breaks a label into lines no wider than max, at spaces, keeping the
 * label's own line breaks. A word wider than max stays whole on its own
 * line; the caller sees it in the width and picks a wider box. */
export function wrap(
  s: string,
  max: number,
  size: number,
  face: Face,
): string[] {
  const out: string[] = [];
  for (const para of s.split("\n")) {
    let line = "";
    for (const word of para.split(" ")) {
      const next = line === "" ? word : `${line} ${word}`;
      if (line !== "" && textWidth(next, size, face) > max) {
        out.push(line);
        line = word;
      } else {
        line = next;
      }
    }
    out.push(line);
  }
  return out;
}
