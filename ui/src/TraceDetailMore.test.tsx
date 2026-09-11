import { StrictMode } from "react";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import Trace from "./Trace";

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);
const t0 = 1_000_000;

describe("Trace, the remaining step details", () => {
  it("says what the answer cost and how much of the material it cited", () => {
    strict(
      <Trace
        steps={[{ step: "writing", at: t0, detail: { prompt_tokens: 26300, completion_tokens: 897, cited: 10, sources: 155 } }]}
        state="done"
        startedAt={t0}
        endedAt={t0 + 100}
      />,
    );
    expect(screen.getByText(/26.3k tokens in · 897 tokens out · 10 of 155 sources cited/)).toBeTruthy();
  });

  it("names a pinned thread's scope, a corpus-wide ask, and the repositories left out", () => {
    const { rerender } = strict(
      <Trace
        steps={[{ step: "understanding", at: t0, detail: { repos: ["peeq"], pinned: true, unknown_repos: ["loom"], outside_repos: ["rongo"] } }]}
        state="running"
        startedAt={t0}
      />,
    );
    expect(screen.getByText(/peeq, the thread's scope/)).toBeTruthy();
    expect(screen.getByText(/not indexed: loom/)).toBeTruthy();
    expect(screen.getByText(/outside this thread: rongo/)).toBeTruthy();

    rerender(
      <StrictMode>
        <Trace steps={[{ step: "understanding", at: t0, detail: { all_repos: true } }]} state="running" startedAt={t0} />
      </StrictMode>,
    );
    expect(screen.getByText(/every indexed project, as the question asked/)).toBeTruthy();
  });

  it("draws a search that found nothing, and an answer decided by the judge without the rule note", () => {
    const { container, rerender } = strict(
      <Trace steps={[{ step: "searching", at: t0, detail: { hits: 0 } }]} state="running" startedAt={t0} />,
    );
    expect(container.querySelector(".trace-detail")?.textContent).toContain("0 hits");

    rerender(
      <StrictMode>
        <Trace
          steps={[{ step: "routing", at: t0, detail: { decision: "ask", rung: "judge", candidates: ["a", "b"] } }]}
          state="waiting"
          startedAt={t0}
          endedAt={t0 + 1}
        />
      </StrictMode>,
    );
    expect(screen.getByText(/Asking back: the matches are independent alternatives/)).toBeTruthy();
    expect(screen.queryByText(/decided by rule/)).toBeNull();

    rerender(
      <StrictMode>
        <Trace steps={[{ step: "suggesting", at: t0, detail: { anything: 1 } }]} state="done" startedAt={t0} endedAt={t0 + 1} />
      </StrictMode>,
    );
    expect(container.querySelector(".trace-detail")).toBeNull();
  });
});
