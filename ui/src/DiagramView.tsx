import { useEffect, useRef } from "react";
import { useFocusOnOpen } from "./dialog";
import { MermaidSvg, diagramTitle } from "./diagram";
import { download, fileName, toSvgFile } from "./diagramExport";
import { useBackdropDismiss } from "./dismiss";
import { DownloadIcon } from "./icons";

/**
 * DiagramView is the diagram seen whole. In the answer the picture is scaled
 * into a prose column, so a five-actor sequence is read small; here it has
 * the width of the sheet.
 *
 * The shell is SourceView's, down to the z-index: the reader is stepping out
 * of the answer for a moment and goes straight back, and one overlay at a time
 * is what the app does.
 */
export default function DiagramView({
  src,
  svg,
  onClose,
}: {
  src: string;
  svg: string;
  onClose: () => void;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  const dismiss = useBackdropDismiss(onClose);
  const body = useRef<HTMLDivElement>(null);
  const title = diagramTitle(src);

  useFocusOnOpen(closeButton);

  // Escape closes, and Tab stays inside. SourceView has one control and can
  // simply refocus it; this dialog has several, so the ends of the ring wrap
  // to each other. Without it, Tab reaches the citation chips in the answer
  // behind the scrim — they are focusable groups — and Enter there would open
  // a second z-30 overlay under this one.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        onClose();
        return;
      }
      if (e.key !== "Tab") return;
      const inside = dialog.current?.querySelectorAll<HTMLElement>("button:not([disabled])");
      if (!inside || inside.length === 0) return;
      const first = inside[0];
      const last = inside[inside.length - 1];
      const on = document.activeElement;
      if (!e.shiftKey && (on === last || !dialog.current?.contains(on))) {
        e.preventDefault();
        first.focus();
      } else if (e.shiftKey && (on === first || !dialog.current?.contains(on))) {
        e.preventDefault();
        last.focus();
      }
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  function save() {
    download(fileName(src), toSvgFile({ svg }, body.current));
  }

  return (
    <div
      className="fixed inset-0 z-30 flex items-center justify-center bg-black/55 p-0 sm:p-6 md:p-10"
      // A press beside the sheet closes it and does nothing else: see
      // useBackdropDismiss for why that is the click and not the pointerdown.
      ref={dismiss.ref}
      onPointerDown={dismiss.onPointerDown}
      onPointerUp={dismiss.onPointerUp}
    >
      {/* font-sans explicitly: this is mounted from inside the answer's
          .ui-markdown wrapper, which is serif prose, and the dialog is chrome.
          SourceView needs no such line because Ask.tsx mounts it outside the
          prose. */}
      <div
        ref={dialog}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="grid h-full w-full max-w-[1100px] grid-rows-[auto_1fr] overflow-hidden rounded-none border-0 bg-panel font-sans shadow-panel sm:rounded-ui-lg sm:border sm:border-elevated-border"
      >
        <header className="flex items-center gap-2 border-b border-border px-3 py-2.5 sm:gap-3 sm:px-4.5 sm:py-3">
          <span className="min-w-0 truncate text-[13.5px] text-ink">{title}</span>
          <span className="ml-auto" />
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

        {/* The drawing scales to the sheet's width and no further: the SVG
            carries its own max-width, so a small diagram stays 1:1 and is
            centred, and a wide one fits. */}
        <div ref={body} className="grid min-h-0 place-items-center overflow-auto p-6">
          <div className="w-full">
            <MermaidSvg svg={svg} title={title} />
          </div>
        </div>
      </div>
    </div>
  );
}
