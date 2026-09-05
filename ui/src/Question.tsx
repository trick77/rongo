import { useLayoutEffect, useRef, useState } from "react";

/**
 * The question a turn was asked, as it was typed.
 *
 * Not a headline: nothing caps a question's length — the composer takes
 * Shift+Enter and the server only trims — so a question can run to several
 * paragraphs, and setting those in the 26px accent serif the turn used to
 * wear filled a screen before the answer started. It reads as prose at the
 * answer's own 68ch measure instead, against a quiet rule, with the eyebrow
 * above saying whose words they are.
 *
 * whitespace-pre-wrap because the breaks are the reader's: the text is stored
 * and exported with its paragraphs, and a plain <p> collapsed them into one
 * unbroken run on screen.
 *
 * Long ones fold. Three lines and a fade, then the whole thing on a click —
 * the answer stays where the eye expects it on a thread reopened to re-read
 * it, rather than sitting below a wall of one's own words. The full text is in
 * the DOM either way, so copy, export and find-in-page never see the fold.
 */
export default function Question({ text }: { text: string }) {
  const box = useRef<HTMLDivElement>(null);
  // Whether the clamp is actually cutting anything. Measured rather than
  // guessed from the length: a question wraps differently at every width, and
  // the toggle must appear exactly when there is something behind it.
  const [clipped, setClipped] = useState(false);
  const [open, setOpen] = useState(false);

  // A turn is keyed by its position in the thread, so switching threads hands
  // this same instance another thread's question. Without the reset, a
  // question left unfolded leaves the next one unfolded too — and the next one
  // measures as unclipped only after the effect runs, which the open branch
  // skips, so a one-line question kept a "Show less" under it.
  const [shown, setShown] = useState(text);
  if (shown !== text) {
    setShown(text);
    setOpen(false);
    setClipped(false);
  }

  useLayoutEffect(() => {
    // Only while folded: open, the clamp is off and the box measures as
    // unclipped, which would take the "Show less" away mid-read.
    if (open) return;
    const el = box.current;
    if (!el) return;
    const measure = () => setClipped(el.scrollHeight - el.clientHeight > 1);
    measure();
    // A narrower window wraps the same question into more lines. No
    // ResizeObserver (jsdom, older Safari) simply means the first measure
    // stands.
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [open, text]);

  return (
    <div className="mt-2 max-w-[68ch] border-l-2 border-elevated pl-4">
      <div className="relative">
        <div
          ref={box}
          // 4.5em is three lines of this leading. A max-height rather than
          // line-clamp: the text is one node with its own newlines in it, and
          // line-clamp counts boxes, not wrapped lines of pre-wrap text.
          className={
            "font-serif text-[19px] leading-[1.5] whitespace-pre-wrap text-ink-prose " +
            (open ? "" : "max-h-[4.5em] overflow-hidden")
          }
        >
          {text}
        </div>
        {/* The last visible line runs out under the page's own ground rather
            than stopping at a hard edge, the way a rail row's title does. */}
        {!open && clipped && (
          <span
            aria-hidden="true"
            className="pointer-events-none absolute inset-x-0 bottom-0 h-10 bg-gradient-to-b from-transparent to-bg"
          />
        )}
      </div>
      {clipped && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
          className="mt-2 text-[13px] text-muted transition-colors hover:text-ink"
        >
          {open ? "Show less" : "Show the whole question"}
        </button>
      )}
    </div>
  );
}
