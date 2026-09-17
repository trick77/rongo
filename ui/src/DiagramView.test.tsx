import { describe, it, expect, vi } from "vitest";
import { render, fireEvent, createEvent } from "@testing-library/react";
import DiagramView from "./DiagramView";

const src = "sequenceDiagram\n  Ask->>httpapi: POST /api/ask\n  httpapi-->>Ask: stream";
const svg = '<svg viewBox="0 0 300 120" style="max-width: 300px"><g class="drawn"></g></svg>';

function view(onClose: () => void = () => {}) {
  return render(<DiagramView src={src} svg={svg} onClose={onClose} />);
}

describe("DiagramView", () => {
  it("opens with the focus on its close button", () => {
    const { getByLabelText } = view();
    expect(document.activeElement).toBe(getByLabelText("Close"));
  });

  it("names the picture in its header", () => {
    const { getByRole } = view();
    expect(getByRole("dialog").getAttribute("aria-label")).toBe("Sequence diagram");
    expect(getByRole("dialog").querySelector("svg .drawn")).toBeTruthy();
  });

  it("closes on Escape", () => {
    const onClose = vi.fn();
    view(onClose);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("closes when the scrim itself is clicked, not the sheet", () => {
    // The click, not the press: the sheet is gone by the time the click is
    // dispatched, and it would otherwise land on the answer behind it.
    const onClose = vi.fn();
    const { getByRole } = view(onClose);
    const dialog = getByRole("dialog");
    const scrim = dialog.parentElement as HTMLElement;

    fireEvent(dialog, createEvent.pointerDown(dialog, { bubbles: true }));
    fireEvent.click(dialog);
    expect(onClose).not.toHaveBeenCalled();

    fireEvent(scrim, createEvent.pointerDown(scrim, { bubbles: true }));
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.click(scrim);
    expect(onClose).toHaveBeenCalled();
  });

  it("takes a drag across the edge for a drag, not a cancel", () => {
    const onClose = vi.fn();
    const { getByRole } = view(onClose);
    const dialog = getByRole("dialog");
    const scrim = dialog.parentElement as HTMLElement;

    fireEvent(dialog, createEvent.pointerDown(dialog, { bubbles: true }));
    fireEvent(scrim, createEvent.pointerUp(scrim, { bubbles: true }));
    fireEvent.click(scrim);
    expect(onClose).not.toHaveBeenCalled();

    fireEvent(scrim, createEvent.pointerDown(scrim, { bubbles: true }));
    fireEvent(dialog, createEvent.pointerUp(dialog, { bubbles: true }));
    fireEvent.click(scrim);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("keeps Tab inside, so it cannot reach the chips behind the scrim", () => {
    // Those chips are focusable groups; Enter on one would open SourceView
    // under this dialog, two z-30 overlays deep.
    const { getByRole, getByLabelText, getByText } = view();
    const dialog = getByRole("dialog");
    const close = getByLabelText("Close");
    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(document, { key: "Tab" });
    expect(dialog.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).toBe(getByText("Download SVG"));
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("draws its chrome in the sans face, not the answer's serif prose", () => {
    const { getByRole } = view();
    expect(getByRole("dialog").className).toContain("font-sans");
  });

  it("downloads the picture it is showing", () => {
    const create = vi.fn(() => "blob:x");
    Object.defineProperty(URL, "createObjectURL", { value: create, configurable: true });
    Object.defineProperty(URL, "revokeObjectURL", { value: vi.fn(), configurable: true });
    let name = "";
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      name = this.download;
    });
    const { getByText } = view();
    fireEvent.click(getByText("Download SVG"));
    expect(create).toHaveBeenCalled();
    expect(name).toBe("rongo-sequence-diagram.svg");
    click.mockRestore();
  });
});
