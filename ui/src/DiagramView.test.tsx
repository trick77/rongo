import { describe, it, expect, vi, beforeAll } from "vitest";
import { render, fireEvent, createEvent } from "@testing-library/react";
import DiagramView from "./DiagramView";
import { diagramSize, type SequenceSpec } from "./diagram";

const seq: SequenceSpec = {
  type: "sequence",
  actors: [
    { id: "ui", label: "Ask.tsx" },
    { id: "api", label: "httpapi" },
    { id: "ask", label: "Answerer" },
  ],
  steps: [
    { from: "ui", to: "api", label: "POST /api/ask", kind: "call", src: [1] },
    { from: "api", to: "ask", label: "Answer()", kind: "call", src: [2] },
    { from: "ask", to: "ui", label: "stream", kind: "return", src: [3] },
  ],
};

/** jsdom has no ResizeObserver and measures nothing, so the box the view
 * fits into is stated here. The layout itself never measures either
 * (diagram.tsx), so the scale a test sees is the scale a browser computes. */
function observing(width: number, height: number) {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(width);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(height);
}

beforeAll(() => {
  observing(400, 400);
});

describe("DiagramView", () => {
  it("opens with the focus on its close button", () => {
    const { getByLabelText } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    expect(document.activeElement).toBe(getByLabelText("Close"));
  });

  it("closes on Escape", () => {
    const onClose = vi.fn();
    render(<DiagramView spec={seq} hooks={{}} onClose={onClose} />);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("closes when the scrim itself is pressed, not the sheet", () => {
    const onClose = vi.fn();
    const { getByRole } = render(<DiagramView spec={seq} hooks={{}} onClose={onClose} />);
    const dialog = getByRole("dialog");
    fireEvent(dialog, createEvent.pointerDown(dialog, { bubbles: true }));
    expect(onClose).not.toHaveBeenCalled();
    const scrim = dialog.parentElement as HTMLElement;
    fireEvent(scrim, createEvent.pointerDown(scrim, { bubbles: true }));
    expect(onClose).toHaveBeenCalled();
  });

  it("keeps Tab inside, so it cannot reach the chips behind the scrim", () => {
    // Those chips are focusable groups; Enter on one would open SourceView
    // under this dialog, two z-30 overlays deep.
    const { getByRole, getByLabelText, getByText } = render(
      <DiagramView spec={seq} hooks={{}} onClose={() => {}} />,
    );
    const dialog = getByRole("dialog");
    const close = getByLabelText("Close");
    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(document, { key: "Tab" });
    expect(dialog.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).toBe(getByText("Fit"));
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("draws its chrome in the sans face, not the answer's serif prose", () => {
    const { getByRole } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    expect(getByRole("dialog").className).toContain("font-sans");
  });

  it("scales a diagram wider than the sheet down to fit, never up", () => {
    const { container } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    const size = diagramSize(seq);
    const scale = Number(container.querySelector("[data-scale]")?.getAttribute("data-scale"));
    expect(size.width).toBeGreaterThan(400 - 48);
    expect(scale).toBeCloseTo((400 - 48) / size.width, 5);
    expect(scale).toBeLessThan(1);
  });

  it("reserves the scaled size, so a fitted diagram does not also scroll", () => {
    const { container } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    const inner = container.querySelector("[data-scale]") as HTMLElement;
    const scale = Number(inner.getAttribute("data-scale"));
    const size = diagramSize(seq);
    expect(inner.style.transform).toBe(`scale(${scale})`);
    expect((inner.parentElement as HTMLElement).style.width).toBe(`${size.width * scale}px`);
  });

  it("offers Fit when only the height is too much for the sheet", () => {
    // A long sequence in a short window is scaled by its height, and the
    // toggle has to be there to undo it.
    const tall: SequenceSpec = {
      ...seq,
      steps: Array.from({ length: 14 }, (_, i) => ({
        from: "ui" as const,
        to: "api" as const,
        label: `step ${i}`,
        kind: "call" as const,
        src: [],
      })),
    };
    observing(2000, 300);
    const { container, getByText } = render(<DiagramView spec={tall} hooks={{}} onClose={() => {}} />);
    expect(diagramSize(tall).width).toBeLessThan(2000 - 48);
    expect(Number(container.querySelector("[data-scale]")?.getAttribute("data-scale"))).toBeLessThan(1);
    expect(getByText("Fit")).toBeTruthy();
    observing(400, 400);
  });

  it("opens at 1:1 on a phone, rather than shrinking the chips out of reach", () => {
    // At 390px fitting this diagram lands near 0.34, and the 10px chips with
    // it. The phone scrolls the picture, as the card behind it does.
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    const { container, getByText } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    expect(container.querySelector("[data-scale]")?.getAttribute("data-scale")).toBe("1");
    // And the toggle is there to fit it on purpose.
    expect(getByText("Fit")).toBeTruthy();
    vi.unstubAllGlobals();
  });

  it("still fits by default where there is room for it", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    const { container } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    expect(Number(container.querySelector("[data-scale]")?.getAttribute("data-scale"))).toBeLessThan(1);
    vi.unstubAllGlobals();
  });

  it("keeps Fit reachable on a phone, where it scales hardest", () => {
    const { getByText } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    expect(getByText("Fit").className).not.toContain("hidden");
  });

  it("gives the diagram back its own size when Fit is turned off", () => {
    const { container, getByText } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    fireEvent.click(getByText("Fit"));
    const inner = container.querySelector("[data-scale]") as HTMLElement;
    expect(inner.getAttribute("data-scale")).toBe("1");
    expect(inner.style.transform).toBe("");
  });

  it("stands aside before the source it was asked for opens", () => {
    const order: string[] = [];
    const onClose = () => order.push("close");
    const onOpen = () => order.push("open");
    const { container } = render(
      <DiagramView spec={seq} hooks={{ onOpen, backed: new Set([1, 2, 3]) }} onClose={onClose} />,
    );
    const chip = container.querySelector('g[role="button"]') as SVGGElement;
    fireEvent.click(chip);
    expect(order).toEqual(["close", "open"]);
  });

  it("downloads the picture it is showing", () => {
    const create = vi.fn(() => "blob:x");
    Object.defineProperty(URL, "createObjectURL", { value: create, configurable: true });
    Object.defineProperty(URL, "revokeObjectURL", { value: vi.fn(), configurable: true });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const { getByText } = render(<DiagramView spec={seq} hooks={{}} onClose={() => {}} />);
    fireEvent.click(getByText("Download SVG"));
    expect(create).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    click.mockRestore();
  });
});
