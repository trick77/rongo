import { useCallback, useEffect, useRef } from "react";

/**
 * Dismissal for a full-screen overlay whose scrim closes it.
 *
 * On the click, not on the press: closing on pointerdown unmounts the overlay
 * before the browser dispatches the click, and that click is then delivered to
 * whatever the scrim was covering — a citation row, which opens the viewer
 * again, or the Ask button. The click is the last event of the gesture, so
 * nothing is left to leak into the page behind.
 *
 * The listener sits on the scrim element itself rather than on React's
 * delegated root: iOS Safari does not bubble a click out of a plain div, and
 * an iPad has no Escape key to fall back on.
 *
 * Only a press that started on the scrim counts. A drag that began inside the
 * dialog — selecting code, panning a diagram — and ended out here is a drag,
 * not a cancel.
 */
export function useBackdropDismiss(onClose: () => void) {
  const scrim = useRef<HTMLDivElement>(null);
  const startedOnScrim = useRef(false);
  // The overlays pass a fresh arrow every render; the listener reads the
  // latest one instead of being torn down and rebound each time.
  const close = useRef(onClose);
  close.current = onClose;

  useEffect(() => {
    const el = scrim.current;
    if (!el) return;
    function onClick(e: MouseEvent) {
      if (e.target !== el || !startedOnScrim.current) return;
      startedOnScrim.current = false;
      close.current();
    }
    el.addEventListener("click", onClick);
    return () => el.removeEventListener("click", onClick);
  }, []);

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    startedOnScrim.current = e.target === e.currentTarget;
  }, []);

  return { ref: scrim, onPointerDown };
}
