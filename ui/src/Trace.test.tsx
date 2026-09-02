import { StrictMode } from "react";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Trace from "./Trace";

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

describe("Trace", () => {
  it("shows one collapsed line with the running step and a chevron", () => {
    strict(<Trace steps={["Understand the question", "Gather code"]} state="running" />);

    expect(screen.getByText(/Gather code/)).toBeTruthy();
    expect(screen.queryByText("Understand the question")).toBeNull();
    expect(screen.getByRole("button").querySelector("svg")).toBeTruthy();
  });

  it("shows every step as a node on one continuous line when expanded", async () => {
    const user = userEvent.setup();
    strict(<Trace steps={["Understand the question", "Gather code"]} state="running" />);

    await user.click(screen.getByRole("button"));

    expect(screen.getByText("Understand the question")).toBeTruthy();
    expect(screen.getByText("Gather code")).toBeTruthy();
  });

  it("ends a finished turn on the done node", async () => {
    const user = userEvent.setup();
    strict(<Trace steps={["Understand the question"]} state="done" />);

    await user.click(screen.getByRole("button"));

    expect(screen.getByText("Done")).toBeTruthy();
    expect(screen.queryByText("Waiting for a choice")).toBeNull();
  });

  it("ends a clarification on the ochre waiting node, not the check", async () => {
    // loom has no third state: complete = !active && !streaming would claim
    // "done" while a person is still being waited on.
    const user = userEvent.setup();
    strict(<Trace steps={["Understand the question"]} state="waiting" />);

    await user.click(screen.getByRole("button"));

    const waiting = screen.getByText("Waiting for a choice");
    expect(waiting).toBeTruthy();
    expect(screen.queryByText("Done")).toBeNull();
    const marker = waiting.closest("li")?.querySelector("span[aria-hidden]");
    expect(marker?.getAttribute("class")).toContain("ochre");
  });

  it("is announced politely as a panel", () => {
    strict(<Trace steps={["Understand the question"]} state="running" />);

    const status = screen.getByRole("status");
    expect(status.getAttribute("aria-live")).toBe("polite");
  });
});
