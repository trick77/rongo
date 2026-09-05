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

  it("locks every candidate once the card is answered", async () => {
    // One card, one answer. The reopened card is a record of what was
    // decided, not a second chance to decide it.
    const onChoose = vi.fn();
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={onChoose} />);

    await user.click(screen.getByRole("button", { name: /Chosen/ }));
    await user.click(screen.getByText("Through the legacy adapter"));
    await user.click(screen.getByText("Through the login service"));

    expect(onChoose).not.toHaveBeenCalled();
    for (const title of ["Through the login service", "Through the legacy adapter"]) {
      expect(screen.getByText(title).closest("button")?.hasAttribute("disabled")).toBe(true);
    }
  });

  // A repository card's last entry stands for every repository at once, so it
  // carries no repo and no branch. Printing the pair anyway leaves a bare "·"
  // where an identifier should be, which reads as a missing value.
  const repoCandidates = [
    { idx: 0, title: "Token cost per turn", summary: "Prices a turn.", repo: "peeq", branch: "master" },
    { idx: 1, title: "Per-request cost", summary: "Costs one request.", repo: "loom", branch: "master" },
    { idx: 2, title: "All repositories", summary: "Answer across every indexed repository.", repo: "", branch: "" },
  ];

  it("offers the all-repositories entry without a repo line", async () => {
    const onChoose = vi.fn();
    const user = userEvent.setup();
    strict(<Clarify candidates={repoCandidates} onChoose={onChoose} />);

    const entry = screen.getByText("All repositories").closest("button");
    expect(entry?.textContent).not.toContain("·");
    expect(screen.getByText("Token cost per turn").closest("button")?.textContent).toContain("peeq · master");

    await user.click(screen.getByText("All repositories"));
    expect(onChoose).toHaveBeenCalledWith(2);
  });

  it("records the all-repositories choice without a bare separator", () => {
    strict(<Clarify candidates={repoCandidates} chosenIdx={2} onChoose={() => {}} />);

    const header = screen.getByRole("button", { name: /Chosen/ });
    expect(header.textContent).toContain("Chosen: All repositories");
    expect(header.textContent).not.toContain("·");
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
