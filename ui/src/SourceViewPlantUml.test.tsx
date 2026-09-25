import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// The engine stands in as in plantuml.test.ts; the drawing itself is checked
// in a browser.
const renderToString = vi.fn();
vi.mock("@plantuml/core", () => ({ renderToString }));
vi.mock("@plantuml/core/themes.js", () => ({}));
// jsdom has no canvas to encode with: the PNG itself is tested in
// diagramExport.test.ts.
const { toPng } = vi.hoisted(() => ({ toPng: vi.fn() }));
vi.mock("./diagramExport", async (original) => ({ ...(await original<object>()), toPng }));

import SourceView from "./SourceView";

const source = {
  marker: 1,
  repo: "shop",
  branch: "main",
  path: "doc/Order.puml",
  start_line: 2,
  end_line: 2,
  sha: "0123abcdef",
};

function serve(content: string) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ content, sha: "0123abcdef", branch: "main" }) })),
  );
}

beforeEach(() => {
  (globalThis as Record<string, unknown>).Viz = {};
  renderToString.mockReset();
});
afterEach(() => vi.unstubAllGlobals());

describe("SourceView on a PlantUML file", () => {
  it("opens as the picture, and the source with the cited lines is one click away", async () => {
    serve("@startuml\nUser -> Api : order\n@enduml\n");
    renderToString.mockImplementation((_l: string[], ok: (s: string) => void) =>
      ok('<svg xmlns="http://www.w3.org/2000/svg"><text>order</text></svg>'),
    );
    const user = userEvent.setup();

    render(<SourceView source={source} onClose={() => {}} />);

    const picture = await screen.findByRole("img", { name: "Diagram" });
    expect(picture.querySelector("svg text")?.textContent).toBe("order");
    expect(document.querySelector("[data-line]")).toBeNull();
    expect(screen.getByRole("button", { name: "Diagram" }).getAttribute("aria-pressed")).toBe("true");

    await user.click(screen.getByRole("button", { name: "Source" }));

    expect(screen.queryByRole("img", { name: "Diagram" })).toBeNull();
    const hits = document.querySelectorAll("[data-hit]");
    expect(Array.from(hits).map((h) => h.getAttribute("data-line"))).toEqual(["2"]);
  });

  it("numbers the pictures of a file holding several", async () => {
    serve("@startuml\nA -> B\n@enduml\n@startuml\nC -> D\n@enduml\n");
    renderToString.mockImplementation((_l: string[], ok: (s: string) => void) => ok("<svg></svg>"));

    render(<SourceView source={source} onClose={() => {}} />);

    expect(await screen.findByRole("img", { name: "Diagram 1 of 2" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "Diagram 2 of 2" })).toBeTruthy();
  });

  it("offers the source when the engine fails", async () => {
    serve("@startuml\nbroken engine\n@enduml\n");
    renderToString.mockImplementation((_l: string[], _ok: unknown, fail: (m: string) => void) => fail("no canvas"));
    const user = userEvent.setup();

    render(<SourceView source={source} onClose={() => {}} />);

    expect((await screen.findByRole("alert")).textContent).toContain("no canvas");
    await user.click(screen.getByRole("button", { name: "Show the source" }));
    await waitFor(() => expect(document.querySelector('[data-line="2"]')).not.toBeNull());
  });

  it("keeps Tab inside the dialog, around the views, the close button and the downloads", async () => {
    serve("@startuml\nTab -> Ring\n@enduml\n");
    renderToString.mockImplementation((_l: string[], ok: (s: string) => void) => ok("<svg></svg>"));
    const user = userEvent.setup();

    render(<SourceView source={source} onClose={() => {}} />);
    await screen.findByRole("img", { name: "Diagram" });
    const close = screen.getByRole("button", { name: "Close" });
    const diagram = screen.getByRole("button", { name: "Diagram" });
    const src = screen.getByRole("button", { name: "Source" });
    const svg = screen.getByRole("button", { name: "Diagram: download SVG" });
    const png = screen.getByRole("button", { name: "Diagram: download PNG" });

    expect(document.activeElement).toBe(close);
    await user.tab();
    expect(document.activeElement).toBe(svg);
    await user.tab();
    expect(document.activeElement).toBe(png);
    await user.tab();
    expect(document.activeElement).toBe(diagram);
    await user.tab();
    expect(document.activeElement).toBe(src);
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(diagram);
  });

  describe("downloads", () => {
    let saved: { name: string; blob: Blob }[];
    beforeEach(() => {
      saved = [];
      let blob: Blob | null = null;
      Object.defineProperty(URL, "createObjectURL", {
        value: (b: Blob) => ((blob = b), "blob:x"),
        configurable: true,
      });
      Object.defineProperty(URL, "revokeObjectURL", { value: () => {}, configurable: true });
      vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
        saved.push({ name: this.download, blob: blob as unknown as Blob });
      });
      toPng.mockReset();
    });
    afterEach(() => vi.restoreAllMocks());

    it("takes each picture away as an SVG on white, named after the file", async () => {
      // Text of its own: drawings are kept per text for the session.
      serve("@startuml\nSave -> A\n@enduml\n@startuml\nC -> D\n@enduml\n");
      renderToString.mockImplementation((l: string[], ok: (s: string) => void) =>
        ok(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 20"><text>${l[1]}</text></svg>`),
      );
      const user = userEvent.setup();

      render(<SourceView source={source} onClose={() => {}} />);
      await screen.findByRole("img", { name: "Diagram 1 of 2" });
      await user.click(screen.getByRole("button", { name: "Diagram 1 of 2: download SVG" }));
      await user.click(screen.getByRole("button", { name: "Diagram 2 of 2: download SVG" }));

      expect(saved.map((s) => s.name)).toEqual(["Order.svg", "Order-2.svg"]);
      const text = await saved[1].blob.text();
      expect(text).toContain('<rect x="0" y="0" width="40" height="20" fill="#FFFFFF"/>');
      expect(text).toContain("C -&gt; D");
    });

    it("takes a picture away as a PNG, and says so when it cannot", async () => {
      serve("@startuml\nSave -> Png\n@enduml\n");
      renderToString.mockImplementation((_l: string[], ok: (s: string) => void) => ok("<svg></svg>"));
      const png = new Blob(["png"], { type: "image/png" });
      toPng.mockResolvedValueOnce(png).mockRejectedValueOnce(new Error("the picture is too large for a PNG"));
      const user = userEvent.setup();

      render(<SourceView source={source} onClose={() => {}} />);
      const button = await screen.findByRole("button", { name: "Diagram: download PNG" });
      await user.click(button);
      await waitFor(() => expect(saved).toEqual([{ name: "Order.png", blob: png }]));
      expect(screen.queryByRole("alert")).toBeNull();

      await user.click(button);
      expect((await screen.findByRole("alert")).textContent).toContain("too large for a PNG");
      expect(saved).toHaveLength(1);
    });
  });

  it("offers no views on a file that is not a diagram", async () => {
    serve("package main\n");
    render(<SourceView source={{ ...source, path: "main.go", start_line: 1, end_line: 1 }} onClose={() => {}} />);
    await waitFor(() => expect(document.querySelector('[data-line="1"]')).not.toBeNull());
    expect(screen.queryByRole("button", { name: "Diagram" })).toBeNull();
  });
});
