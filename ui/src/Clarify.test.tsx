import { StrictMode } from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Clarify from "./Clarify";

// Every test mounts under StrictMode, the way the real app renders it.
const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

const candidates = [
  {
    idx: 0,
    title: "Through the login service",
    summary: "Sign-in runs through the central login service.",
    repo: "peeq",
    branch: "master",
  },
  {
    idx: 1,
    title: "Through the legacy adapter",
    summary: "The old adapter signs users in straight against LDAP.",
    repo: "peeq-legacy",
    branch: "release-2024.3",
  },
];

describe("Clarify", () => {
  it("names every candidate with its repo and one line", () => {
    strict(<Clarify candidates={candidates} onChoose={() => {}} />);

    expect(screen.getByText("Through the login service")).toBeTruthy();
    expect(screen.getByText("peeq · master")).toBeTruthy();
    expect(screen.getByText(/central login service/)).toBeTruthy();

    expect(screen.getByText("Through the legacy adapter")).toBeTruthy();
    expect(screen.getByText("peeq-legacy · release-2024.3")).toBeTruthy();
    expect(screen.getByText(/against LDAP/)).toBeTruthy();
  });

  it("reports the candidate index back on a choice", async () => {
    const onChoose = vi.fn();
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} onChoose={onChoose} />);

    await user.click(screen.getByText("Through the legacy adapter"));

    expect(onChoose).toHaveBeenCalledWith(1);
  });

  it("collapses the card to one line after the choice and stays in the thread", () => {
    // The decision belongs in the record: sometimes you only notice from the
    // answer that you picked the wrong mechanism, which is why the card
    // stays — just collapsed.
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={() => {}} />);

    expect(screen.getByText(/Chosen: Through the login service/)).toBeTruthy();
    expect(screen.queryByText(/central login service/)).toBeNull();
    expect(screen.queryByText("Through the legacy adapter")).toBeNull();
  });

  it("shows every candidate when the collapsed card is reopened, the chosen one marked", async () => {
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={() => {}} />);

    await user.click(screen.getByRole("button", { name: /Chosen/ }));

    expect(screen.getByText("Through the login service")).toBeTruthy();
    expect(screen.getByText("Through the legacy adapter")).toBeTruthy();
    const chosenButton = screen.getByText("Through the login service").closest("button");
    expect(chosenButton?.textContent).toContain("Chosen");
  });

  it("leaves another candidate clickable after the choice - it starts a new turn", async () => {
    const onChoose = vi.fn();
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={onChoose} />);

    await user.click(screen.getByRole("button", { name: /Chosen/ }));
    await user.click(screen.getByText("Through the legacy adapter"));

    expect(onChoose).toHaveBeenCalledWith(1);
  });

  it("rotates the chevron by 90 degrees on open, without swapping the glyph", async () => {
    // AGENTS.md: chevron only — no triangle, no plus/minus, no glyph swap.
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={() => {}} />);

    const button = screen.getByRole("button", { name: /Chosen/ });
    const pathBefore = button.querySelector("svg path")?.getAttribute("d");
    expect(button.querySelector("svg")?.getAttribute("class")).not.toContain("rotate-90");

    await user.click(button);

    const pathAfter = button.querySelector("svg path")?.getAttribute("d");
    expect(button.querySelector("svg")?.getAttribute("class")).toContain("rotate-90");
    expect(pathAfter).toBe(pathBefore);
  });
});
