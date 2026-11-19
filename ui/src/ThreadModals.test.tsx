import { describe, it, expect, vi } from "vitest";
import { createEvent, fireEvent, render } from "@testing-library/react";

import { ModalShell } from "./ThreadModals";

function shell(onCancel = vi.fn()) {
  const view = render(
    <ModalShell title="Rename thread" onCancel={onCancel}>
      <input aria-label="Title" />
    </ModalShell>,
  );
  const dialog = view.getByRole("dialog");
  return { dialog, scrim: dialog.parentElement as HTMLElement, onCancel };
}

describe("ModalShell", () => {
  it("cancels on a click that starts and ends on the scrim", () => {
    const { scrim, onCancel } = shell();

    fireEvent(scrim, createEvent.pointerDown(scrim, { bubbles: true }));
    fireEvent(scrim, createEvent.pointerUp(scrim, { bubbles: true }));
    fireEvent.click(scrim);

    expect(onCancel).toHaveBeenCalled();
  });

  it("takes a drag out of the dialog for a drag, not a cancel", () => {
    // Selecting the half-typed title and letting go beside the card: the
    // browser dispatches that click on the scrim, the nearest common ancestor.
    const { dialog, scrim, onCancel } = shell();

    fireEvent(dialog, createEvent.pointerDown(dialog, { bubbles: true }));
    fireEvent(scrim, createEvent.pointerUp(scrim, { bubbles: true }));
    fireEvent.click(scrim);

    expect(onCancel).not.toHaveBeenCalled();
  });
});
