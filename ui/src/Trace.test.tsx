import { StrictMode } from "react";
import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import Trace, { stepLabel } from "./Trace";

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

const t0 = 1_000_000;
const steps = [
  { step: "understanding", at: t0 },
  { step: "gathering", at: t0 + 800 },
];

describe("Trace", () => {
  it("shows every step as a node on one continuous line, with nothing to shut", () => {
    // Progress is watched, never shut: while the turn runs there is no toggle on
    // screen at all, because the reader twice rejected a trace that hid live steps.
    const { container } = strict(<Trace steps={steps} state="running" startedAt={t0} />);

    expect(screen.getByText("Understanding the question")).toBeTruthy();
    expect(screen.getByText("Reading the code")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
    expect(container.querySelector(".trace-steps-open")).toBeTruthy();
  });

  it("rolls the steps up behind the closing row once the turn ends", () => {
    const { container, rerender } = render(
      <StrictMode>
        <Trace steps={steps} state="running" startedAt={t0} />
      </StrictMode>,
    );
    expect(container.querySelector(".trace-steps-open")).toBeTruthy();

    rerender(
      <StrictMode>
        <Trace steps={steps} state="done" startedAt={t0} endedAt={t0 + 2300} />
      </StrictMode>,
    );

    // The closing row stays: it is the row the reader is looking at, and the
    // toggle. The steps behind it are collapsed, not unmounted.
    expect(screen.getByText("Done")).toBeTruthy();
    expect(container.querySelector(".trace-steps-open")).toBeNull();
    expect(container.querySelector(".trace-steps")?.getAttribute("aria-hidden")).toBe("true");
    expect(screen.getByRole("button").getAttribute("aria-expanded")).toBe("false");
  });

  it("opens again on the chevron", () => {
    const { container, rerender } = render(
      <StrictMode>
        <Trace steps={steps} state="running" startedAt={t0} />
      </StrictMode>,
    );
    rerender(
      <StrictMode>
        <Trace steps={steps} state="done" startedAt={t0} endedAt={t0 + 2300} />
      </StrictMode>,
    );

    fireEvent.click(screen.getByRole("button"));

    expect(container.querySelector(".trace-steps-open")).toBeTruthy();
    expect(container.querySelector(".trace-steps")?.getAttribute("aria-hidden")).toBeNull();
  });

  it("does not shut a trace the reader opened, on a later state change", () => {
    // The roll-up fires on the running -> closed transition, once. A clarification
    // being answered is not a second excuse to close what the reader opened.
    const { container, rerender } = render(
      <StrictMode>
        <Trace steps={steps} state="running" startedAt={t0} />
      </StrictMode>,
    );
    rerender(
      <StrictMode>
        <Trace steps={steps} state="waiting" startedAt={t0} endedAt={t0 + 2300} />
      </StrictMode>,
    );
    fireEvent.click(screen.getByRole("button"));

    rerender(
      <StrictMode>
        <Trace steps={steps} state="decided" startedAt={t0} endedAt={t0 + 2300} />
      </StrictMode>,
    );

    expect(container.querySelector(".trace-steps-open")).toBeTruthy();
  });

  it("marks the running step, and only that one, as the current node", () => {
    strict(<Trace steps={steps} state="running" startedAt={t0} />);

    const rows = screen.getAllByRole("listitem");
    expect(rows[1].querySelector(".node-now")).toBeTruthy();
    expect(rows[0].querySelector(".node-now")).toBeNull();
    expect(screen.queryByText("Done")).toBeNull();
  });

  it("shows how long each finished step took", () => {
    strict(<Trace steps={steps} state="done" startedAt={t0} endedAt={t0 + 2300} />);

    expect(screen.getByText("0.8s")).toBeTruthy(); // understanding → gathering
    expect(screen.getByText("1.5s")).toBeTruthy(); // gathering → closed
    expect(screen.getByText("2.3s")).toBeTruthy(); // the whole turn
  });

  it("ends a finished turn on the done node", () => {
    strict(<Trace steps={steps} state="done" startedAt={t0} endedAt={t0 + 2300} />);

    expect(screen.getByText("Done")).toBeTruthy();
    expect(screen.queryByText("Waiting for a choice")).toBeNull();
  });

  it("ends a clarification on the ochre waiting node, not the check", () => {
    // loom has no third state: complete = !active && !streaming would claim
    // "done" while a person is still being waited on.
    strict(<Trace steps={steps} state="waiting" startedAt={t0} endedAt={t0 + 2300} />);

    const waiting = screen.getByText("Waiting for a choice");
    expect(waiting).toBeTruthy();
    expect(screen.queryByText("Done")).toBeNull();
    const marker = waiting.closest("li")?.querySelector("span[aria-hidden]");
    expect(marker?.getAttribute("class")).toContain("ochre");
  });

  it("loses the ochre once the choice has been made", () => {
    // Ochre means "your move". After the move it is a record, not a prompt.
    strict(<Trace steps={steps} state="decided" startedAt={t0} endedAt={t0 + 2300} />);

    const row = screen.getByText("Asked back, choice made").closest("li");
    expect(row?.querySelector("span[aria-hidden]")?.getAttribute("class")).not.toContain("ochre");
    expect(screen.queryByText("Waiting for a choice")).toBeNull();
  });

  it("ends a broken turn on the failure node", () => {
    strict(<Trace steps={steps} state="failed" startedAt={t0} endedAt={t0 + 2300} />);

    expect(screen.getByText("The turn failed")).toBeTruthy();
    expect(screen.queryByText("Done")).toBeNull();
  });

  it("is announced politely as a panel", () => {
    strict(<Trace steps={steps} state="running" startedAt={t0} />);

    const status = screen.getByRole("status");
    expect(status.getAttribute("aria-live")).toBe("polite");
  });

  it("shows a step the backend has no label for as it came", () => {
    expect(stepLabel("answering")).toBe("Thinking about the answer");
    expect(stepLabel("writing")).toBe("Writing the answer");
    expect(stepLabel("verstehen")).toBe("verstehen");
  });
});
