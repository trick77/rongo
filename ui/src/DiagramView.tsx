import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { DiagramSvg, diagramSize, diagramTitle, type DiagramSpec } from "./diagram";
import { download, fileName, toSvgFile } from "./diagramExport";
import { DownloadIcon } from "./icons";
import type { MarkerHooks } from "./markdown";

/**
 * DiagramView is the diagram seen whole. In the answer the picture is drawn at
 * its intrinsic width and the card scrolls sideways, so a five-actor sequence
 * is read a slice at a time; here it is one picture.
 *
 * The shell is SourceView's, down to the z-index: the reader is stepping out
 * of the answer for a moment and goes straight back, and one overlay at a time
 * is what the app does. That is also why a citation chip in here closes the
 * view before opening its source — SourceView is z-30 as well and traps Tab
 * and Escape on the document, so stacked, one Escape would close both.
 */
export default function DiagramView({
  spec,
  hooks,
  onClose,
}: {
  spec: DiagramSpec;
  hooks: MarkerHooks;
  onClose: () => void;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const svg = useRef<SVGSVGElement>(null);
  const [fit, setFit] = useState(true);
  const [box, setBox] = useState({ width: 0, height: 0 });
  const title = diagramTitle(spec);
  const size = diagramSize(spec);

  // Focus moves into the dialog on open and back to where it was on close, as
  // SourceView does.
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    closeButton.current?.focus();
    return () => before?.focus?.();
  }, []);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  // The available box, measured rather than assumed: the sheet is a share of
  // the window, and the window is anything from a phone to a desktop.
  useLayoutEffect(() => {
    const el = body.current;
    if (!el) return;
    function read() {
      if (el) setBox({ width: el.clientWidth, height: el.clientHeight });
    }
    read();
    if (typeof ResizeObserver !== "function") return;
    const ro = new ResizeObserver(read);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Down only, never up: a small diagram blown up to fill the sheet is a
  // blurry-looking drawing with 30px labels, and the point here is to see the
  // whole picture, not a bigger one.
  const room = { width: box.width - 48, height: box.height - 48 };
  const scale =
    fit && room.width > 0 && room.height > 0
      ? Math.min(1, room.width / size.width, room.height / size.height)
      : 1;
  // Either dimension: a long sequence in a short window is scaled down by its
  // height, and a toggle that stayed hidden would leave the reader with a
  // shrunken picture and no way back to 1:1.
  const overflows = room.width > 0 && (size.width > room.width || size.height > room.height);

  function save() {
    if (svg.current) download(fileName(spec), toSvgFile(svg.current));
  }

  return (
    <div
      className="fixed inset-0 z-30 flex items-center justify-center bg-black/55 p-0 sm:p-6 md:p-10"
      // The pointer, not the mouse: iOS Safari does not deliver mouse events
      // to a plain div, and an iPad has no Escape key to fall back on.
      onPointerDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="grid h-full w-full max-w-[1100px] grid-rows-[auto_1fr] overflow-hidden rounded-none border-0 bg-panel shadow-panel sm:rounded-ui-lg sm:border sm:border-elevated-border"
      >
        <header className="flex items-center gap-2 border-b border-border px-3 py-2.5 sm:gap-3 sm:px-4.5 sm:py-3">
          <span className="min-w-0 truncate text-[13.5px] text-ink">{title}</span>
          <span className="ml-auto" />
          {/* Only offered when there is something to fit: on a diagram that
              already stands whole in the sheet the toggle would do nothing,
              and a control that does nothing reads as broken. */}
          {/* Never hidden on a phone, unlike the pills beside it: that is the
              width where fitting scales a wide diagram hardest, and this is
              the only way back to the size the labels were drawn at. */}
          {(overflows || !fit) && (
            <button
              type="button"
              onClick={() => setFit(!fit)}
              aria-pressed={fit}
              className={
                "flex h-8 items-center rounded-ui-sm border px-2.5 text-[13px] " +
                (fit
                  ? "border-elevated-border bg-active text-ink"
                  : "border-border text-ink-dim hover:border-elevated-border hover:bg-active")
              }
            >
              Fit
            </button>
          )}
          <button
            type="button"
            onClick={save}
            className="hidden h-8 items-center gap-1.5 rounded-ui-sm border border-border px-2.5 text-[13px] text-ink-dim hover:border-elevated-border hover:bg-active sm:flex"
          >
            <DownloadIcon />
            Download SVG
          </button>
          <button
            ref={closeButton}
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="grid h-11 w-11 place-items-center rounded-ui-sm text-lg leading-none text-muted hover:bg-active hover:text-ink sm:h-8 sm:w-8"
          >
            ×
          </button>
        </header>

        <div ref={body} className="grid min-h-0 place-items-center overflow-auto p-6">
          {/* A transform scales the drawing without touching its layout box,
              so the wrapper carries the scaled size itself — left to reserve
              the intrinsic width, a fitted diagram would still scroll while
              looking as though it fits. */}
          <div
            style={
              scale === 1
                ? undefined
                : { width: size.width * scale, height: size.height * scale }
            }
          >
            <div
              style={scale === 1 ? undefined : { transform: `scale(${scale})`, transformOrigin: "0 0" }}
              data-scale={scale}
            >
              <DiagramSvg
                spec={spec}
                hooks={{
                  ...hooks,
                  // The source opens where the reader can see it: behind this
                  // scrim the Sources pane is invisible, so the view stands
                  // aside first.
                  onOpen: hooks.onOpen
                    ? (m) => {
                        onClose();
                        hooks.onOpen?.(m);
                      }
                    : undefined,
                  // Nothing to hover on to: the pane is behind the scrim.
                  onHover: undefined,
                }}
                svgRef={svg}
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
