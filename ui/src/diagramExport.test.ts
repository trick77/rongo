import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { toMermaid, mermaidize, toSvgFile, fileName, withGround, svgSize, pngScale, toPng, toXml, phoneCanvas } from "./diagramExport";
import type { FlowSpec, SequenceSpec } from "./diagram";

const seq: SequenceSpec = {
  type: "sequence",
  actors: [
    { id: "repos.yaml", label: "repos.yaml" },
    { id: "repos-yaml", label: "Startup" },
    { id: "idx", label: "Indexer" },
  ],
  steps: [
    { from: "repos.yaml", to: "repos-yaml", label: "Load repository list", kind: "call", src: [1, 2] },
    { from: "repos-yaml", to: "idx", label: "Sync specs", kind: "async", src: [] },
    { from: "idx", to: "idx", label: "Keyword indexing", kind: "call", src: [8] },
    { from: "idx", to: "repos-yaml", label: "Store files", kind: "return", src: [9] },
  ],
};

const flow: FlowSpec = {
  type: "flow",
  nodes: [
    { id: "a", label: "Answer()", kind: "start", src: [3] },
    { id: "b", label: 'sources "empty"?', kind: "decision", src: [] },
    { id: "c", label: "NothingFound", kind: "end", src: [] },
    { id: "d", label: "Stream to the model", kind: "step", src: [4] },
  ],
  edges: [
    { from: "a", to: "b" },
    { from: "b", to: "c", label: "yes [3]" },
    { from: "b", to: "d", label: "no" },
  ],
};

describe("toMermaid", () => {
  it("writes a sequence with one participant per actor", () => {
    const out = toMermaid(seq);
    expect(out.split("\n")[0]).toBe("sequenceDiagram");
    expect(out).toContain("participant idx as Indexer");
  });

  it("keeps ids apart when sanitizing collides them", () => {
    const out = toMermaid(seq);
    // "repos.yaml" and "repos-yaml" both sanitize to repos_yaml.
    expect(out).toContain("participant repos_yaml as repos.yaml");
    expect(out).toContain("participant repos_yaml_2 as Startup");
    expect(out).toContain("repos_yaml->>repos_yaml_2: Load repository list [1] [2]");
  });

  it("draws call, async and return with their own arrows", () => {
    const out = toMermaid(seq);
    expect(out).toContain("repos_yaml_2-)idx: Sync specs");
    expect(out).toContain("idx-->>repos_yaml_2: Store files [9]");
  });

  it("keeps a self-message on one actor", () => {
    expect(toMermaid(seq)).toContain("idx->>idx: Keyword indexing [8]");
  });

  it("carries the markers so they still match the sources block", () => {
    expect(toMermaid(seq)).toContain("[1] [2]");
  });

  it("renames an id mermaid reads as syntax", () => {
    // "end" is the flow spec's own node kind, so it is what a model writes.
    // Left alone it closes a block in a sequence and fails to parse in a
    // flowchart, taking the picture with it.
    const out = toMermaid({
      type: "flow",
      nodes: [
        { id: "start", label: "Begin", kind: "start", src: [] },
        { id: "end", label: "Done", kind: "end", src: [] },
      ],
      edges: [{ from: "start", to: "end" }],
    });
    expect(out).toContain('end_(["Done"])');
    expect(out).toContain("start --> end_");
    expect(out).not.toMatch(/^\s+end\(/m);
  });

  it("falls back to the id when an actor has no label", () => {
    const out = toMermaid({
      type: "sequence",
      actors: [{ id: "ui", label: "" }],
      steps: [{ from: "ui", to: "ui", label: "tick", kind: "call", src: [] }],
    });
    expect(out).toContain("participant ui as ui");
    expect(out).not.toMatch(/ as\s*$/m);
  });

  it("writes a flow with the kind in the node shape", () => {
    const out = toMermaid(flow);
    expect(out.split("\n")[0]).toBe("flowchart TD");
    expect(out).toContain('a(["Answer() [3]"])');
    expect(out).toContain('d["Stream to the model [4]"]');
    expect(out).toContain("{");
  });

  it("escapes a quote inside a label rather than ending it early", () => {
    expect(toMermaid(flow)).toContain("#quot;empty#quot;");
  });

  it("quotes an edge label so a marker survives the parser", () => {
    expect(toMermaid(flow)).toContain('a --> b');
    expect(toMermaid(flow)).toContain('b -->|"yes [3]"| c');
  });
});

describe("mermaidize", () => {
  const body = JSON.stringify({
    type: "sequence",
    actors: [{ id: "ui", label: "Ask.tsx" }],
    steps: [{ from: "ui", to: "ui", label: "render", kind: "call", src: [1] }],
  });

  it("replaces a diagram fence with a mermaid one", () => {
    const out = mermaidize(`Before [1].\n\n\`\`\`diagram\n${body}\n\`\`\`\n\nAfter.`);
    expect(out).toContain("```mermaid");
    expect(out).toContain("sequenceDiagram");
    expect(out).not.toContain("```diagram");
    expect(out).not.toContain('"actors"');
    expect(out).toContain("Before [1].");
    expect(out).toContain("After.");
  });

  it("leaves prose and other fences alone", () => {
    const src = "Text.\n\n```go\nfunc main() {}\n```\n";
    expect(mermaidize(src)).toBe(src);
  });

  it("retags a diagram fence holding mermaid text, which draws here and nowhere else", () => {
    const src = "```diagram\nflowchart TD\n  a --> b\n```\n";
    expect(mermaidize(src)).toBe("```mermaid\nflowchart TD\n  a --> b\n```\n");
  });

  it("leaves an unclosed diagram fence as the text it is", () => {
    const src = "```diagram\n{half";
    expect(mermaidize(src)).toBe(src);
  });
});

describe("toSvgFile", () => {
  afterEach(() => {
    document.head.querySelectorAll("style[data-test]").forEach((s) => s.remove());
  });

  const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="-8 -25 240 90"><style>.x{}</style><rect width="10" height="10"/></svg>';

  function card(): HTMLElement {
    const style = document.createElement("style");
    style.setAttribute("data-test", "1");
    style.textContent = ".card { background-color: #1b1b1a; }";
    document.head.appendChild(style);
    const host = document.createElement("div");
    host.className = "card";
    document.body.appendChild(host);
    return host;
  }

  it("carries the card's ground, so the pale labels are not lost on white", () => {
    const out = toSvgFile({ svg }, card());
    // Over the viewBox, which starts above and left of the origin.
    expect(out).toContain('<rect x="-8" y="-25" width="240" height="90" fill="rgb(27, 27, 26)"/>');
    // First, so it sits behind the drawing rather than over it.
    expect(out.indexOf('fill="rgb(27, 27, 26)"')).toBeLessThan(out.indexOf('width="10"'));
  });

  it("leaves the renderer's file alone otherwise", () => {
    expect(toSvgFile({ svg }, null)).toBe(svg);
    const out = toSvgFile({ svg }, card());
    expect(out).toContain('viewBox="-8 -25 240 90"');
    expect(out).toContain("<style>.x{}</style>");
  });
});

describe("fileName", () => {
  it("names the file after the kind of picture", () => {
    expect(fileName("sequenceDiagram\n A->>B: x")).toBe("rongo-sequence-diagram.svg");
    expect(fileName("flowchart TD\n a")).toBe("rongo-flowchart-diagram.svg");
    expect(fileName("graph LR\n a")).toBe("rongo-flowchart-diagram.svg");
    expect(fileName("stateDiagram-v2\n a")).toBe("rongo-state-diagram.svg");
    expect(fileName("erDiagram\n A")).toBe("rongo-er-diagram.svg");
    expect(fileName("")).toBe("rongo-diagram.svg");
  });
});

describe("download", () => {
  it("hands the browser a blob and lets it go again", async () => {
    const { download } = await import("./diagramExport");
    const create = vi.fn(() => "blob:x");
    const revoke = vi.fn();
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    Object.defineProperty(URL, "createObjectURL", { value: create, configurable: true });
    Object.defineProperty(URL, "revokeObjectURL", { value: revoke, configurable: true });
    download("d.svg", "<svg/>");
    expect(create).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    expect(revoke).toHaveBeenCalledWith("blob:x");
    click.mockRestore();
  });

  it("hands over a blob it was given as it is", async () => {
    const { download } = await import("./diagramExport");
    const create = vi.fn((_b: Blob) => "blob:y");
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    Object.defineProperty(URL, "createObjectURL", { value: create, configurable: true });
    Object.defineProperty(URL, "revokeObjectURL", { value: vi.fn(), configurable: true });
    const png = new Blob(["x"], { type: "image/png" });
    download("d.png", png);
    expect(create).toHaveBeenCalledWith(png);
    click.mockRestore();
  });
});

describe("withGround", () => {
  it("lays the colour over the viewBox, behind the drawing", () => {
    expect(withGround('<svg viewBox="0 0 30 20"><rect width="4"/></svg>', "#FFFFFF")).toBe(
      '<svg viewBox="0 0 30 20"><rect x="0" y="0" width="30" height="20" fill="#FFFFFF"/><rect width="4"/></svg>',
    );
  });
});

describe("svgSize", () => {
  it("reads the pixel size the engine wrote", () => {
    expect(svgSize('<svg width="120px" height="80px" viewBox="0 0 240 160">')).toEqual({ w: 120, h: 80 });
  });

  it("falls back to the viewBox, and knows when there is neither", () => {
    expect(svgSize('<svg viewBox="-5 -5 240 160">')).toEqual({ w: 240, h: 160 });
    expect(svgSize("<svg>")).toBeNull();
  });
});

describe("pngScale", () => {
  it("draws at twice the size where the canvas allows it", () => {
    expect(pngScale(600, 400)).toBe(2);
  });

  it("stays inside what a browser canvas holds", () => {
    // The file that first overflowed the engine's own cap.
    const s = pngScale(4788, 13531);
    expect(13531 * s).toBeLessThanOrEqual(16384);
    expect(s).toBeGreaterThan(1);
    expect(40000 * pngScale(40000, 100)).toBeLessThanOrEqual(16384);
  });

  it("fits a phone's smaller canvas when asked to", () => {
    const s = pngScale(2500, 2000, phoneCanvas);
    expect(2500 * s * 2000 * s).toBeLessThanOrEqual(phoneCanvas.area);
  });
});

describe("toXml", () => {
  it("writes the drawing as XML, which a file must be", () => {
    // What DOMPurify hands back is HTML: a no-break space comes out as
    // &nbsp;, an entity no SVG viewer knows.
    const html = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 4 4"><text>a&nbsp;b</text><br></svg>';
    const out = toXml(html);
    expect(out).not.toContain("&nbsp;");
    const doc = new DOMParser().parseFromString(out, "image/svg+xml");
    expect(doc.querySelector("parsererror")).toBeNull();
    expect(doc.querySelector("text")?.textContent).toBe("a b");
    expect(out).toContain('viewBox="0 0 4 4"');
  });
});

describe("toPng", () => {
  let blob: Blob | null;
  let fail: boolean;
  const ctx = { fillStyle: "", fillRect: vi.fn(), drawImage: vi.fn() };

  beforeEach(() => {
    blob = new Blob(["png"], { type: "image/png" });
    fail = false;
    ctx.fillStyle = "";
    ctx.fillRect.mockReset();
    ctx.drawImage.mockReset();
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(ctx as unknown as CanvasRenderingContext2D);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((cb: BlobCallback) => cb(blob));
    // jsdom loads no images: this one answers as soon as it has a source.
    vi.stubGlobal(
      "Image",
      class {
        onload: (() => void) | null = null;
        onerror: (() => void) | null = null;
        set src(_v: string) {
          setTimeout(() => (fail ? this.onerror?.() : this.onload?.()), 0);
        }
      },
    );
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("paints the drawing on white at the scale the canvas allows", async () => {
    expect(await toPng('<svg width="100px" height="50px"></svg>')).toBe(blob);
    expect(ctx.fillStyle).toBe("#FFFFFF");
    expect(ctx.fillRect).toHaveBeenCalledWith(0, 0, 200, 100);
    expect(ctx.drawImage).toHaveBeenCalledWith(expect.anything(), 0, 0, 200, 100);
  });

  it("fails when the picture has no size, will not load, or will not encode", async () => {
    await expect(toPng("<svg></svg>")).rejects.toThrow();
    fail = true;
    await expect(toPng('<svg width="10" height="10"></svg>')).rejects.toThrow();
    fail = false;
    blob = null;
    await expect(toPng('<svg width="10" height="10"></svg>')).rejects.toThrow();
  });

  it("fails rather than hangs when the canvas throws", async () => {
    ctx.drawImage.mockImplementation(() => {
      throw new Error("SecurityError");
    });
    await expect(toPng('<svg width="10" height="10"></svg>')).rejects.toThrow("SecurityError");
  });

  it("draws again at a phone's size when the large canvas is refused", async () => {
    vi.mocked(HTMLCanvasElement.prototype.getContext)
      .mockReturnValueOnce(null)
      .mockReturnValue(ctx as unknown as CanvasRenderingContext2D);
    expect(await toPng('<svg width="3000" height="3000"></svg>')).toBe(blob);
    const [, , w, h] = ctx.fillRect.mock.calls[0];
    expect(w * h).toBeLessThanOrEqual(phoneCanvas.area);
    expect(w).toBeGreaterThan(3000);
  });
});
