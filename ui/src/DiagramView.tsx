import { useRef } from "react";
import { MermaidSvg, diagramTitle } from "./diagram";
import { download, fileName, toSvgFile } from "./diagramExport";
import OverlayShell from "./OverlayShell";
import { DownloadIcon } from "./icons";

/**
 * DiagramView is the diagram seen whole. In the answer the picture is scaled
 * into a prose column, so a five-actor sequence is read small; here it has
 * the width of the sheet.
 *
 * The shell is OverlayShell, as for a file or a commit.
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
  const body = useRef<HTMLDivElement>(null);
  const title = diagramTitle(src);

  function save() {
    download(fileName(src), toSvgFile({ svg }, body.current));
  }

  return (
    <OverlayShell
      label={title}
      // font-sans explicitly: this is mounted from inside the answer's
      // .ui-markdown wrapper, which is serif prose, and the dialog is chrome.
      // SourceView needs no such line because Ask.tsx mounts it outside the
      // prose.
      dialogClassName="grid h-full w-full max-w-[1100px] grid-rows-[auto_1fr] overflow-hidden rounded-none border-0 bg-panel font-sans shadow-panel sm:rounded-ui-lg sm:border sm:border-elevated-border"
      headerClassName="flex items-center gap-2 border-b border-border px-3 py-2.5 sm:gap-3 sm:px-4.5 sm:py-3"
      onClose={onClose}
      header={
        <>
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
        </>
      }
    >
      {/* The drawing scales to the sheet's width and no further: the SVG
          carries its own max-width, so a small diagram stays 1:1 and is
          centred, and a wide one fits. */}
      <div ref={body} className="grid min-h-0 place-items-center overflow-auto p-6">
        <div className="w-full">
          <MermaidSvg svg={svg} title={title} />
        </div>
      </div>
    </OverlayShell>
  );
}
