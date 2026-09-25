import { describe, it, expect, vi, beforeEach } from "vitest";

// The engine lays out with a WASM Graphviz and measures text with a canvas,
// neither of which jsdom has. It is stood in for here; the real engine was
// measured in Chromium (see plantuml.ts), and the viewer is checked there.
const renderToString = vi.fn();
vi.mock("@plantuml/core", () => ({ renderToString }));
vi.mock("@plantuml/core/themes.js", () => ({}));

import { clean, diagrams, drawPlantUml, isPlantUml } from "./plantuml";

beforeEach(() => {
  renderToString.mockReset();
  // The layout engine's global, present: no script tag is added.
  (globalThis as Record<string, unknown>).Viz = {};
});

describe("isPlantUml", () => {
  it("takes the diagram extensions and leaves include fragments alone", () => {
    expect(isPlantUml("doc/Entities.puml")).toBe(true);
    expect(isPlantUml("doc/flow.PlantUML")).toBe(true);
    expect(isPlantUml("a.pu")).toBe(true);
    expect(isPlantUml("doc/style.iuml")).toBe(false);
    expect(isPlantUml("src/puml.go")).toBe(false);
  });
});

describe("diagrams", () => {
  it("splits a file into its blocks and drops what lies between them", () => {
    const text = "' header\n@startuml\nA -> B\n@enduml\n\nnoise\n@startmindmap\n* root\n@endmindmap\n";
    expect(diagrams(text)).toEqual([
      ["@startuml", "A -> B", "@enduml"],
      ["@startmindmap", "* root", "@endmindmap"],
    ]);
  });

  it("keeps an unclosed block, and hands a file with none over whole", () => {
    expect(diagrams("@startuml\r\nA -> B\r\n")).toEqual([["@startuml", "A -> B", ""]]);
    expect(diagrams("A -> B")).toEqual([["A -> B"]]);
  });
});

describe("clean", () => {
  it("strips script and handlers and keeps the drawing", () => {
    const out = clean('<svg xmlns="http://www.w3.org/2000/svg" onload="x()"><script>x()</script><rect width="4"/></svg>');
    expect(out).not.toContain("script");
    expect(out).not.toContain("onload");
    expect(out).toContain("<rect");
  });
});

describe("drawPlantUml", () => {
  it("draws every block, one at a time, and closes the engine's own fetches", async () => {
    let running = 0;
    let most = 0;
    renderToString.mockImplementation((lines: string[], ok: (s: string) => void) => {
      most = Math.max(most, ++running);
      setTimeout(() => {
        running--;
        ok(`<svg><text>${lines[1]}</text></svg>`);
      }, 1);
    });

    const out = await drawPlantUml("@startuml\nfirst\n@enduml\n@startuml\nsecond\n@enduml");

    expect(out).toEqual({ svgs: ["<svg><text>first</text></svg>", "<svg><text>second</text></svg>"] });
    expect(most).toBe(1);
    const loader = (globalThis as Record<string, unknown>).PLANTUML_STDLIB_LOADER as (
      url: string,
      ok: () => void,
      fail: (m: string) => void,
    ) => unknown;
    const fail = vi.fn();
    expect(loader("c4.min.js", () => {}, fail)).toBe(true);
    expect(fail).toHaveBeenCalledWith("c4.min.js is not bundled with rongo");
  });

  it("draws a file once, however often it is opened", async () => {
    renderToString.mockImplementation((_l: string[], ok: (s: string) => void) => ok("<svg></svg>"));
    await drawPlantUml("@startuml\nonce\n@enduml");
    await drawPlantUml("@startuml\nonce\n@enduml");
    expect(renderToString).toHaveBeenCalledTimes(1);
  });

  it("answers an engine failure with its message and tries again next time", async () => {
    renderToString.mockImplementationOnce((_l: string[], _ok: unknown, fail: (m: string) => void) => fail("boom"));
    expect(await drawPlantUml("@startuml\nretry\n@enduml")).toEqual({ error: "boom" });

    renderToString.mockImplementation((_l: string[], ok: (s: string) => void) => ok("<svg></svg>"));
    expect(await drawPlantUml("@startuml\nretry\n@enduml")).toEqual({ svgs: ["<svg></svg>"] });
  });
});

describe("loading the layout engine", () => {
  it("adds its script on the first file, and tries again after a failed load", async () => {
    vi.resetModules();
    delete (globalThis as Record<string, unknown>).Viz;
    const fresh = await import("./plantuml");
    renderToString.mockImplementation((_l: string[], ok: (s: string) => void) => ok("<svg></svg>"));

    const failed = fresh.drawPlantUml("@startuml\nA\n@enduml");
    await vi.waitFor(() => expect(document.head.querySelector("script")).not.toBeNull());
    const first = document.head.querySelector("script") as HTMLScriptElement;
    expect(first.src).toContain("viz-global");
    first.onerror?.(new Event("error"));
    expect(await failed).toEqual({ error: "The diagram engine could not be loaded." });
    expect(document.head.querySelector("script")).toBeNull();

    const drawn = fresh.drawPlantUml("@startuml\nA\n@enduml");
    await vi.waitFor(() => expect(document.head.querySelector("script")).not.toBeNull());
    const second = document.head.querySelector("script") as HTMLScriptElement;
    second.onload?.(new Event("load"));
    expect(await drawn).toEqual({ svgs: ["<svg></svg>"] });
    second.remove();
  });
});
