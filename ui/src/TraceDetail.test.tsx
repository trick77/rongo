import { StrictMode } from "react";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import Trace from "./Trace";

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);
const t0 = 1_000_000;

describe("Trace, what each step found", () => {
  it("draws the understanding's terms, identifiers and scope under its step", () => {
    strict(
      <Trace
        steps={[
          {
            step: "understanding",
            at: t0,
            detail: {
              terms: ["Vorerfassung abschicken"],
              code_terms: ["ControllerVorerfassung", "TOPIC_VORERFASSUNG"],
              repos: ["schadenmeldung-service"],
            },
          },
        ]}
        state="running"
        startedAt={t0}
      />,
    );
    expect(screen.getByText("Vorerfassung abschicken")).toBeTruthy();
    expect(screen.getByText("ControllerVorerfassung")).toBeTruthy();
    expect(screen.getByText(/schadenmeldung-service, named by the question/)).toBeTruthy();
  });

  it("turns the routing rung into a sentence, never its identifier", () => {
    strict(
      <Trace
        steps={[{ step: "routing", at: t0, detail: { decision: "ask", rung: "repository", candidates: ["peeq", "rongo"] } }]}
        state="waiting"
        startedAt={t0}
        endedAt={t0 + 100}
      />,
    );
    expect(screen.getByText(/Asking back: the matches span several projects/)).toBeTruthy();
    expect(screen.queryByText(/repository/)).toBeNull();
    expect(screen.getByText("peeq")).toBeTruthy();
    expect(screen.getByText(/decided by rule, no model/)).toBeTruthy();
  });

  it("says how the code was read and which boundary was crossed", () => {
    strict(
      <Trace
        steps={[
          {
            step: "gathering",
            at: t0,
            detail: {
              hits: 20,
              references: 31,
              crossings: 4,
              sources: 55,
              repos: 2,
              tokens: 22000,
              budget: 24000,
              crossed: [{ from: "shipping", to: "queue-master", via: "destination shipping-task" }],
            },
          },
        ]}
        state="running"
        startedAt={t0}
      />,
    );
    expect(screen.getByText("+31 referenced")).toBeTruthy();
    expect(screen.getByText("+4 across a boundary")).toBeTruthy();
    expect(screen.getByText(/55 sources in 2 repositories/)).toBeTruthy();
    expect(screen.getByText(/22.0k of 24.0k tokens/)).toBeTruthy();
    expect(screen.getByText(/shipping → queue-master/)).toBeTruthy();
  });

  it("draws no detail row for a step that reported none", () => {
    // Every turn stored before the detail existed, and every step that has
    // nothing to say: the label and the duration alone, as before.
    const { container } = strict(
      <Trace steps={[{ step: "searching", at: t0 }]} state="done" startedAt={t0} endedAt={t0 + 100} />,
    );
    expect(container.querySelector(".trace-detail")).toBeNull();
  });
});
