import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Icon, ICONS } from "./Icon";

describe("Icon", () => {
  it("maps a name to the glyph picked from the font", () => {
    // A wrong codepoint renders a different picture, not an error, so the
    // mapping is asserted rather than eyeballed.
    expect(ICONS.code).toBe("\ue048");
  });

  it("renders the glyph at the requested size", () => {
    const { container } = render(<Icon name="code" size="21px" />);
    const span = container.querySelector("span");
    expect(span?.textContent).toBe("\ue048");
    expect(span?.style.fontSize).toBe("21px");
    expect(span?.style.fontFamily).toContain("Anthropic Icons");
  });

  it("hides itself from the accessibility tree without a label", () => {
    const { container } = render(<Icon name="code" />);
    expect(container.querySelector("span")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("becomes an image with a name when it carries the meaning", () => {
    render(<Icon name="code" label="Repositories" />);
    expect(screen.getByRole("img", { name: "Repositories" })).toBeTruthy();
  });
});
