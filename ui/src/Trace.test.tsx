import { StrictMode } from "react";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import Trace, { stepLabel } from "./Trace";

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

const t0 = 1_000_000;
const steps = [
  { step: "understanding", at: t0 },
  { step: "gathering", at: t0 + 800 },
];

describe("Trace", () => {
  it("shows every step as a node on one continuous line, without anything to open", () => {
    // Progress is watched, not opened: the reader twice rejected a trace that
    // hid its steps behind a disclosure.
    strict(<Trace steps={steps} state="running" startedAt={t0} />);

    expect(screen.getByText("Understanding the question")).toBeTruthy();
    expect(screen.getByText("Reading the code")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
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
    expect(stepLabel("answering")).toBe("Writing the answer");
    expect(stepLabel("verstehen")).toBe("verstehen");
  });
});
