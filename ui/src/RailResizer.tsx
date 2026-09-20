import { useLayoutEffect, useRef, useState } from "react";

export const RAIL_MIN = 280;
export const RAIL_MAX = 520;
export const RAIL_DEFAULT = 362;

const STORAGE_KEY = "rongo.rail-width";
const STEP = 16; // px per arrow key
const DOUBLE_TAP_MS = 350;
const DRAG_SLOP = 3; // px of travel before a tap counts as a drag

/**
 * Bounds only. The viewport cap lives in the CSS clamp on --rail-w, so that a narrow
 * window never rewrites the stored preference: clamping here and writing the result
 * back would lose the user's number the first time a tablet rotates to portrait.
 */
export function clampRail(w: number): number {
  return Math.min(Math.max(Math.round(w), RAIL_MIN), RAIL_MAX);
}

/**
 * Safari's private mode throws on storage access, and a rail at the default is
 * better than a blank page. A hand-edited value is clamped rather than trusted.
 */
export function storedRailWidth(): number {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(STORAGE_KEY);
  } catch {
    return RAIL_DEFAULT;
  }
  const n = Number(raw);
  return raw !== null && Number.isFinite(n) && n > 0
    ? clampRail(n)
    : RAIL_DEFAULT;
}

function rememberRailWidth(w: number) {
  try {
    localStorage.setItem(STORAGE_KEY, String(w));
  } catch {
    // See storedRailWidth.
  }
}

/** The rail's on-screen width, which the CSS clamp may hold below the preference. */
function renderedWidth(fallback: number): number {
  const el = document.getElementById("nav-drawer");
  const w = el?.getBoundingClientRect().width ?? 0;
  return w > 0 ? w : fallback; // jsdom lays nothing out
}

/** Write the width to the DOM only. The CSS clamp on --rail-w does the viewport cap. */
function paint(w: number) {
  document.documentElement.style.setProperty("--rail-pref", w + "px");
}

/**
 * Drag handle on the rail's right edge, ported from ../transmission-ui.
 *
 * The width lives here and in one CSS variable, not in App's state: the shell
 * re-renders on every streamed token, and a width held up there would be rewritten
 * mid-drag and snap the edge back. Nothing else reads the number, so this component
 * owns it end to end and writes localStorage once, on release.
 *
 * Pointer Events throughout, so mouse, trackpad, pencil and finger take one code
 * path. Reset is a double-tap read from pointer timestamps rather than onDoubleClick:
 * Safari only synthesises dblclick from a double-tap under conditions we would rather
 * not depend on, and a tablet has no other way back to the default width.
 */
export function RailResizer() {
  const [w, setW] = useState(storedRailWidth);
  const lastDown = useRef(-Infinity);
  // The live width during a drag. State alone is not enough: the pointerup handler
  // closes over the w of the render that ran when the drag began, so committing that
  // would snap the rail back to its pre-drag width.
  const live = useRef(w);
  const moved = useRef(false);
  const active = useRef<number | null>(null); // the pointer that owns the drag
  const start = useRef({ x: 0, w: 0 }); // grab point, so the edge does not jump

  // Layout, not effect: an effect paints after the first frame, so the rail would
  // flash at the default width before the stored preference landed.
  useLayoutEffect(() => {
    paint(w);
    // Mount only. Every later change paints itself through commit().
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  /**
   * `next` is a width the user just expressed on screen, so it is the rendered
   * edge. The preference may be wider: the CSS clamp caps --rail-w at 40vw, and a
   * narrow window must not quietly spend the user's stored number. Widening past
   * the cap therefore keeps the larger preference, while any narrowing is taken at
   * face value because that is a deliberate act.
   */
  function commit(
    next: number,
    intent: "narrow" | "widen" | "exact" = "exact",
  ) {
    live.current = next;
    setW(next);
    paint(next);
    rememberRailWidth(
      intent === "widen" ? Math.max(next, storedRailWidth()) : next,
    );
  }

  function onPointerDown(e: React.PointerEvent<HTMLDivElement>) {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    if (active.current !== null) return; // a drag is running; ignore a second finger
    const now = performance.now();
    if (now - lastDown.current < DOUBLE_TAP_MS) {
      lastDown.current = -Infinity;
      commit(RAIL_DEFAULT);
      return;
    }
    lastDown.current = now;
    active.current = e.pointerId;
    // Seed from the RENDERED width, not the preference: --rail-w is clamped to 40vw,
    // so on a narrow window the two diverge and an offset drag would spend that
    // difference moving nothing.
    start.current = { x: e.clientX, w: renderedWidth(live.current) };
    moved.current = false;
    e.preventDefault();
    // preventDefault suppresses the compatibility mousedown, and with it the focus it
    // would have given us. Focus explicitly, or the arrow keys do nothing until the
    // user tabs to the handle.
    e.currentTarget.focus();
    e.currentTarget.setPointerCapture?.(e.pointerId); // jsdom has no pointer capture
    document.body.classList.add("resizing");
  }

  function onPointerMove(e: React.PointerEvent<HTMLDivElement>) {
    if (active.current !== e.pointerId) return;
    // Below the slop this is still a tap: a touch screen emits a pixel of jitter
    // during one, and treating that as a drag both nudged the edge and disarmed the
    // double-tap reset, the only way a finger has back to the default width.
    const dx = e.clientX - start.current.x;
    if (!moved.current && Math.abs(dx) < DRAG_SLOP) return;
    moved.current = true;
    // Offset from the grab point, not the raw clientX: grabbing the handle off-centre
    // would otherwise snap the border to the pointer by up to half the hit area.
    const next = clampRail(start.current.w + dx);
    live.current = next;
    setW(next);
    paint(next);
  }

  function onPointerUp(e: React.PointerEvent<HTMLDivElement>) {
    if (active.current !== e.pointerId) return;
    active.current = null;
    document.body.classList.remove("resizing");
    // A cancel before the slop is not a tap the user made: leaving the window
    // armed would swallow their next deliberate grab as a double-tap reset.
    if (e.type === "pointercancel") lastDown.current = -Infinity;
    // Commit before releasing capture: releasePointerCapture throws NotFoundError
    // when the pointer is already gone, which is exactly the pointercancel case, and
    // the throw would skip the commit and lose the drag.
    // A completed drag must not arm the double-tap window either: re-grabbing the
    // handle within it to fine-tune would snap the width back to the default.
    if (moved.current) {
      lastDown.current = -Infinity;
      commit(
        live.current,
        live.current >= start.current.w ? "widen" : "narrow",
      );
    }
    try {
      e.currentTarget.releasePointerCapture?.(e.pointerId);
    } catch {
      // Already gone; capture is released implicitly anyway.
    }
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    // Seeded from the RENDERED width for the same reason the drag is: with a
    // preference above the 40vw cap, stepping the preference would walk a number
    // nobody can see and announce it through aria-valuenow, while the edge stood
    // still for several presses.
    const from = renderedWidth(live.current);
    if (e.key === "ArrowLeft") commit(clampRail(from - STEP), "narrow");
    else if (e.key === "ArrowRight") commit(clampRail(from + STEP), "widen");
    else if (e.key === "Home") commit(RAIL_DEFAULT);
    else return;
    e.preventDefault();
  }

  return (
    <div
      // Only from lg, where the rail is the layout. Below it the rail is an
      // off-canvas 300px drawer and this would drag an edge nobody can see.
      className="rail-resizer absolute inset-y-0 z-30 hidden w-2.5 cursor-col-resize touch-none select-none lg:block"
      style={{ left: "calc(var(--rail-w) - 7px)" }}
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize sidebar"
      aria-valuenow={w}
      aria-valuemin={RAIL_MIN}
      aria-valuemax={RAIL_MAX}
      tabIndex={0}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onKeyDown={onKeyDown}
    />
  );
}
