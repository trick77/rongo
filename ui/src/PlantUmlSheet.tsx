import { useEffect, useState, type ReactNode } from "react";
import { drawPlantUml, type Drawn } from "./plantuml";

/**
 * PlantUmlSheet is a PlantUML file drawn, inside SourceView. One picture per
 * @start…/@end… block, top to bottom, each at its own size up to the sheet's
 * width. Invalid PlantUML is not a failure here: the engine draws its own
 * complaint, which is the most useful thing to show. What fails is the
 * engine itself (it did not load), and then the source is one click away.
 */
export default function PlantUmlSheet({ text, onSource }: { text: string; onSource: () => void }): ReactNode {
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
  return (
    <div className="flex flex-col items-center gap-6 px-5 py-3">
      {out.svgs.map((svg, i) => (
        <div
          key={i}
          role="img"
          aria-label={out.svgs.length > 1 ? `Diagram ${i + 1} of ${out.svgs.length}` : "Diagram"}
          // White paper, whatever the theme: see plantuml.ts. The engine
          // writes width and height in pixels, so the picture is capped at
          // the sheet, scaled by the viewBox, never grown past 1:1.
          className="plantuml max-w-full rounded-ui-sm bg-white p-3 [&_svg]:h-auto [&_svg]:max-w-full"
          dangerouslySetInnerHTML={{ __html: svg }}
        />
      ))}
    </div>
  );
}
