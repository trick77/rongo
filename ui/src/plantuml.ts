import DOMPurify from "dompurify";
import vizUrl from "@plantuml/core/viz-global.js?url";

/**
 * A PlantUML file drawn in the reader's browser. The source of a .puml says
 * nothing to an Analyst; the picture it describes does. The engine is
 * PlantUML itself compiled to JavaScript (@plantuml/core), so nothing leaves
 * the page: the file already came from rongo's own checkout, and the drawing
 * is made here, never by plantuml.com or a render service.
 *
 * Measured in Chromium before it went in: a sequence, class or entity diagram
 * in 10-70ms, no script or handler in the output, `!include` of a path and
 * `!includeurl` answered with an error picture and no request. Only the
 * engine's lazy fetches remained (stdlib libraries, themes, emoji and icon
 * sets, fetched from the page's own directory), and those are closed below.
 *
 * Drawn light, on a light sheet, never in the engine's dark mode: a diagram
 * written with `!theme` or skinparams was styled for white paper, and in dark
 * mode its black connectors vanished into the panel (seen on `!theme plain`).
 * The author's colours are the picture.
 */

/** isPlantUml says the file at path is a PlantUML diagram. `.iuml` is left
 * out: it is an include fragment, never a diagram on its own. */
export function isPlantUml(path: string): boolean {
  return /\.(puml|plantuml|pu)$/i.test(path);
}

/** diagrams splits a file into its @start…/@end… blocks: one file may hold
 * several, and the engine draws the first only. A file with no block at all
 * is handed over whole, so the engine says what is wrong with it. */
export function diagrams(text: string): string[][] {
  const lines = text.split("\n").map((l) => l.replace(/\r$/, ""));
  const out: string[][] = [];
  let cur: string[] | null = null;
  for (const line of lines) {
    const t = line.trim();
    if (cur === null) {
      if (/^@start[a-z]+/i.test(t)) cur = [line];
      continue;
    }
    cur.push(line);
    if (/^@end[a-z]+/i.test(t)) {
      out.push(cur);
      cur = null;
    }
  }
  if (cur !== null) out.push(cur);
  return out.length > 0 ? out : [lines];
}

type Engine = {
  renderToString: (
    lines: string[],
    ok: (svg: string) => void,
    fail: (message: string) => void,
  ) => void;
};

/** Everything the engine would fetch on its own is refused before it asks.
 * Themes are bundled with the engine; the standard library (C4, AWS, …),
 * emoji and icon sets are not, and their fetch would hit the page's own
 * directory for a file rongo does not serve. A diagram using one draws with
 * PlantUML's own complaint in the picture. */
function closeLazyFetches(): void {
  const g = globalThis as Record<string, unknown>;
  g.PLANTUML_STDLIB_LOADER = (url: string, _ok: () => void, fail: (message: string) => void) => {
    fail(`${url} is not bundled with rongo`);
    return true;
  };
}

/** The layout engine is a classic script that sets a global, and the engine
 * reads that global; bundled as a module it would export instead. So it
 * goes in as a script tag, once. */
function loadViz(): Promise<void> {
  if ((globalThis as Record<string, unknown>).Viz) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src = vizUrl;
    s.onload = () => resolve();
    s.onerror = () => {
      s.remove();
      reject(new Error("The diagram engine could not be loaded."));
    };
    document.head.appendChild(s);
  });
}

/** Loaded on the first PlantUML file, never with the shell: the engine is
 * about 5 MB and most sessions never open one. A load that failed is tried
 * again by the next file, as the mermaid renderer is. */
let loading: Promise<Engine> | null = null;
function engine(): Promise<Engine> {
  if (!loading) {
    loading = (async () => {
      closeLazyFetches();
      await loadViz();
      await import("@plantuml/core/themes.js");
      return (await import("@plantuml/core")) as Engine;
    })().catch((e: unknown) => {
      loading = null;
      throw e;
    });
  }
  return loading;
}

/** The engine keeps shared state and overwrites a render still running with
 * the next one, so renders queue. */
let queue: Promise<unknown> = Promise.resolve();

function renderOne(e: Engine, lines: string[]): Promise<string> {
  const run = queue.then(
    () =>
      new Promise<string>((resolve, reject) =>
        e.renderToString(lines, resolve, (m) => reject(new Error(m))),
      ),
  );
  queue = run.catch(() => undefined);
  return run;
}

/** The SVG is a repository's content reaching innerHTML. The engine writes
 * no script and no handler (measured), and this is the guard that does not
 * depend on that staying true. */
export function clean(svg: string): string {
  return DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true } });
}

export type Drawn = { svgs: string[] } | { error: string };

/** One drawing per file text, kept for the session; a failure is not kept,
 * so a blip does not follow the file around. */
const drawn = new Map<string, Promise<Drawn>>();

/** drawPlantUml turns a file into one SVG per diagram it holds, or into the
 * reason it could not. Exported for the tests. */
export function drawPlantUml(text: string): Promise<Drawn> {
  let p = drawn.get(text);
  if (!p) {
    p = (async (): Promise<Drawn> => {
      try {
        const e = await engine();
        const svgs: string[] = [];
        for (const lines of diagrams(text)) svgs.push(clean(await renderOne(e, lines)));
        return { svgs };
      } catch (err) {
        drawn.delete(text);
        return { error: err instanceof Error ? err.message : String(err) };
      }
    })();
    drawn.set(text, p);
  }
  return p;
}
