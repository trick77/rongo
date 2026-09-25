import { useEffect, useState, type ReactNode } from "react";
import { drawPlantUml, type Drawn } from "./plantuml";
import { download, toPng, withGround } from "./diagramExport";
import { DownloadIcon } from "./icons";

/**
 * PlantUmlSheet is a PlantUML file drawn, inside SourceView. One picture per
 * @start…/@end… block, top to bottom, each at its own size up to the sheet's
 * width. Invalid PlantUML is not a failure here: the engine draws its own
 * complaint, which is the most useful thing to show. What fails is the
 * engine itself (it did not load), and then the source is one click away.
 */
export default function PlantUmlSheet({
  text,
  path,
  onSource,
}: {
  text: string;
  path: string;
  onSource: () => void;
}): ReactNode {
  const [out, setOut] = useState<Drawn | null>(null);
  useEffect(() => {
    let live = true;
    setOut(null);
    drawPlantUml(text).then((d) => {
      if (live) setOut(d);
    });
    return () => {
      live = false;
    };
  }, [text]);

  if (out === null) return <p className="px-5 py-3 font-sans text-muted">Drawing the diagram…</p>;
  if ("error" in out) {
    return (
      <p role="alert" className="px-5 py-3 font-sans text-muted">
        The diagram could not be drawn ({out.error}).{" "}
        <button type="button" onClick={onSource} className="text-accent-strong underline underline-offset-2">
          Show the source
        </button>
      </p>
    );
  }
  const stem = path.slice(path.lastIndexOf("/") + 1).replace(/\.[^.]*$/, "");
  return (
    <div className="flex flex-col items-center gap-6 px-5 py-3">
      {out.svgs.map((svg, i) => (
        <Sheet
          key={i}
          svg={svg}
          label={out.svgs.length > 1 ? `Diagram ${i + 1} of ${out.svgs.length}` : "Diagram"}
          // The first picture carries the file's name, the rest a number,
          // so a one-diagram file downloads as itself.
          name={i === 0 ? stem : `${stem}-${i + 1}`}
        />
      ))}
    </div>
  );
}

/** Sheet is one picture, with its two ways out. The file is drawn on white
 * like the sheet, never transparent: a viewer's own ground under a diagram
 * styled for white paper is how its connectors got lost in dark mode. */
function Sheet({ svg, label, name }: { svg: string; label: string; name: string }): ReactNode {
  const [failed, setFailed] = useState<string | null>(null);
  const file = () => withGround(svg, "#FFFFFF");

  function png() {
    setFailed(null);
    toPng(file()).then(
      (b) => download(`${name}.png`, b),
      (e: unknown) => setFailed(e instanceof Error ? e.message : String(e)),
    );
  }

  const button =
    "flex h-7 items-center gap-1 rounded-ui-sm px-1.5 font-sans text-[11.5px] font-semibold tracking-wide text-stone-500 hover:bg-stone-100 hover:text-stone-800";
  return (
    <div className="max-w-full rounded-ui-sm bg-white p-3 pt-1.5">
      {/* Always drawn, never revealed on hover, as on the answer's diagram
          card: a phone has no hover. A row of its own, not laid over the
          picture as on that card: a PlantUML diagram starts in its corners
          (participants, the first class), and the buttons hid them. */}
      <div className="mb-1 -mr-1.5 flex items-center justify-end gap-0.5">
        <button
          type="button"
          onClick={() => download(`${name}.svg`, file())}
          title="Download SVG"
          aria-label={`${label}: download SVG`}
          className={button}
        >
          <DownloadIcon />
          SVG
        </button>
        <button type="button" onClick={png} title="Download PNG" aria-label={`${label}: download PNG`} className={button}>
          <DownloadIcon />
          PNG
        </button>
      </div>
      <div
        role="img"
        aria-label={label}
        // White paper, whatever the theme: see plantuml.ts. The engine
        // writes width and height in pixels, so the picture is capped at
        // the sheet, scaled by the viewBox, never grown past 1:1.
        className="plantuml [&_svg]:h-auto [&_svg]:max-w-full"
        dangerouslySetInnerHTML={{ __html: svg }}
      />
      {failed !== null && (
        <p role="alert" className="mt-2 font-sans text-[12.5px] text-stone-600">
          The PNG could not be made ({failed}). The SVG holds the same picture.
        </p>
      )}
    </div>
  );
}
