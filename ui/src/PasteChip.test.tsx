import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import PasteChip from "./PasteChip";

const trace = "panic: boom\nmain.go:12\nmain.go:30";

describe("PasteChip", () => {
  it("says how much was pasted and keeps the text folded", () => {
    render(<PasteChip text={trace} lines={3} />);

    const toggle = screen.getByRole("button", { name: "Pasted text · 3 lines" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(trace, { collapseWhitespace: false })).toBeNull();
  });

  it("opens the text under the chip on a click, and closes it again", async () => {
    render(<PasteChip text={trace} lines={3} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Pasted text · 3 lines" }));

    expect(screen.getByRole("button", { name: "Pasted text · 3 lines" }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText(trace, { collapseWhitespace: false }).tagName).toBe("PRE");

    await user.click(screen.getByRole("button", { name: "Pasted text · 3 lines" }));
    expect(screen.queryByText(trace, { collapseWhitespace: false })).toBeNull();
  });

  it("counts one line in the singular", () => {
    render(<PasteChip text={"x".repeat(3000)} lines={1} />);
    expect(screen.getByRole("button", { name: "Pasted text · 1 line" })).toBeTruthy();
  });

  it("offers a remove only in the composer", async () => {
    const onRemove = vi.fn();
    const { rerender } = render(<PasteChip text={trace} lines={3} onRemove={onRemove} />);

    await userEvent.setup().click(screen.getByRole("button", { name: "Remove pasted text" }));
    expect(onRemove).toHaveBeenCalledTimes(1);

    rerender(<PasteChip text={trace} lines={3} />);
    expect(screen.queryByRole("button", { name: "Remove pasted text" })).toBeNull();
  });
});
