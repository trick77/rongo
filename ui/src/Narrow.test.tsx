import { StrictMode } from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Narrow from "./Narrow";

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

const repos = [
  { repo: "peeq", branch: "master" },
  { repo: "loom", branch: "main" },
  { repo: "ledger", branch: "main" },
  { repo: "gateway", branch: "main" },
  { repo: "ingest", branch: "main" },
];

describe("Narrow", () => {
  it("names every repository that matched, not just the ones a card would fit", () => {
    strict(<Narrow repos={repos} onAsk={() => {}} />);

    for (const r of repos) {
      expect(screen.getByRole("button", { name: new RegExp(r.repo) })).toBeTruthy();
    }
    expect(screen.getByText(/5 repositories/)).toBeTruthy();
  });

  it("offers no way to answer across all of them", () => {
    // The whole point of the panel: five repositories' worth of hits spread
    // over five repositories answers none of them.
    strict(<Narrow repos={repos} onAsk={() => {}} />);

    expect(screen.queryByText(/all repositories/i)).toBeNull();
  });

  it("asks with every repository the reader picked", async () => {
    const onAsk = vi.fn();
    const user = userEvent.setup();
    strict(<Narrow repos={repos} onAsk={onAsk} />);

    await user.click(screen.getByRole("button", { name: /peeq/ }));
    await user.click(screen.getByRole("button", { name: /ledger/ }));
    await user.click(screen.getByRole("button", { name: /^Ask/ }));

    expect(onAsk).toHaveBeenCalledWith(["peeq", "ledger"]);
  });

  it("takes a repository back off the list when it is picked again", async () => {
    const onAsk = vi.fn();
    const user = userEvent.setup();
    strict(<Narrow repos={repos} onAsk={onAsk} />);

    await user.click(screen.getByRole("button", { name: /peeq/ }));
    await user.click(screen.getByRole("button", { name: /loom/ }));
    await user.click(screen.getByRole("button", { name: /peeq/ }));
    await user.click(screen.getByRole("button", { name: /^Ask/ }));

    expect(onAsk).toHaveBeenCalledWith(["loom"]);
  });

  it("stops at three, because a fourth side competes for the same room", async () => {
    const user = userEvent.setup();
    strict(<Narrow repos={repos} onAsk={() => {}} />);

    for (const name of ["peeq", "loom", "ledger"]) {
      await user.click(screen.getByRole("button", { name: new RegExp(name) }));
    }

    // The three picked stay live, so the reader can swap one out.
    expect(screen.getByRole("button", { name: /peeq/ }).hasAttribute("disabled")).toBe(false);
    for (const name of ["gateway", "ingest"]) {
      expect(screen.getByRole("button", { name: new RegExp(name) }).hasAttribute("disabled")).toBe(true);
    }
  });

  it("asks for nothing until something is picked", () => {
    strict(<Narrow repos={repos} onAsk={() => {}} />);

    expect(screen.getByRole("button", { name: /^Ask/ }).hasAttribute("disabled")).toBe(true);
  });

  it("collapses to what it narrowed to once the turn went on", () => {
    // The panel stays in the thread for the reason the card does: it is the
    // record of a decision, and the answer below it only makes sense beside
    // the repositories it was narrowed to.
    strict(<Narrow repos={repos} narrowedTo={["peeq", "ledger"]} onAsk={() => {}} />);

    const header = screen.getByRole("button", { name: /Narrowed to/ });
    expect(header.textContent).toContain("peeq");
    expect(header.textContent).toContain("ledger");
    expect(screen.queryByRole("button", { name: /^Ask/ })).toBeNull();
  });

  it("locks every repository once the panel has been answered", async () => {
    const onAsk = vi.fn();
    const user = userEvent.setup();
    strict(<Narrow repos={repos} narrowedTo={["peeq"]} onAsk={onAsk} />);

    await user.click(screen.getByRole("button", { name: /Narrowed to/ }));
    await user.click(screen.getByRole("button", { name: /loom/ }));

    expect(onAsk).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: /loom/ }).hasAttribute("disabled")).toBe(true);
  });


  it("asks nobody to narrow on a page where nobody can", async () => {
    // A shared thread is read: the ochre and the buttons would leave a reader
    // looking for a way to answer a question that is not theirs to answer.
    const onAsk = vi.fn();
    const user = userEvent.setup();
    strict(<Narrow repos={repos} readOnly onAsk={onAsk} />);

    expect(screen.queryByText("That is too broad to ask about.")).toBeNull();
    expect(screen.getByText("Asked back: too broad to answer")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Ask these/ })).toBeNull();

    await user.click(screen.getByRole("button", { name: /peeq/ }));
    expect(onAsk).not.toHaveBeenCalled();
  });

  it("rotates the chevron by 90 degrees on open, without swapping the glyph", async () => {
    const user = userEvent.setup();
    strict(<Narrow repos={repos} narrowedTo={["peeq"]} onAsk={() => {}} />);

    const header = screen.getByRole("button", { name: /Narrowed to/ });
    const before = header.querySelector("svg path")?.getAttribute("d");
    expect(header.querySelector("svg")?.getAttribute("class")).not.toContain("rotate-90");

    await user.click(header);

    expect(header.querySelector("svg")?.getAttribute("class")).toContain("rotate-90");
    expect(header.querySelector("svg path")?.getAttribute("d")).toBe(before);
  });
});
