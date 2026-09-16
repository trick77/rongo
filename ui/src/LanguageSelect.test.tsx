import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import LanguageSelect from "./LanguageSelect";

function pill() {
  return screen.getByRole("button", { name: "Answer language" });
}

describe("LanguageSelect", () => {
  it("names the current language and opens the list on click", async () => {
    const user = userEvent.setup();
    render(<LanguageSelect value="de" onChange={() => {}} />);
    expect(pill().textContent).toBe("Deutsch");
    expect(pill().getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("listbox")).toBeNull();

    await user.click(pill());
    expect(pill().getAttribute("aria-expanded")).toBe("true");
    const rows = screen.getAllByRole("option");
    expect(rows.map((r) => r.textContent)).toEqual(["English", "Deutsch", "Français", "Italiano"]);
    expect(rows[1].getAttribute("aria-selected")).toBe("true");
  });

  it("picks a row with the pointer and closes", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<LanguageSelect value="en" onChange={onChange} />);
    await user.click(pill());
    await user.click(screen.getByRole("option", { name: "Français" }));
    expect(onChange).toHaveBeenCalledWith("fr");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(document.activeElement).toBe(pill());
  });

  it("walks the list with the arrows and picks with Enter", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<LanguageSelect value="en" onChange={onChange} />);
    pill().focus();
    await user.keyboard("{ArrowDown}");
    expect(pill().getAttribute("aria-activedescendant")).toBe("lang-option-en");
    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect(pill().getAttribute("aria-activedescendant")).toBe("lang-option-fr");
    await user.keyboard("{ArrowUp}{Enter}");
    expect(onChange).toHaveBeenCalledWith("de");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("closes on Escape and on a pointer outside, without choosing", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <div>
        <LanguageSelect value="en" onChange={onChange} />
        <p>elsewhere</p>
      </div>,
    );
    await user.click(pill());
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(document.activeElement).toBe(pill());

    await user.click(pill());
    await user.click(screen.getByText("elsewhere"));
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(onChange).not.toHaveBeenCalled();
  });
});
