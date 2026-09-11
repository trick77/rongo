import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StatsPane } from "./StatsPane";
import ThreadUsageBadge from "./ThreadUsageBadge";
import { storedTurn, type Turn, type Usage } from "./turns";

/* The pane on its own, with turns built the way the record builds them.
 * Driving it through Ask proves the wiring; this proves what it says. */

function turnOf(question: string, usage: Usage | null, steps: unknown[] = []): Turn {
  const t = storedTurn({
    id: 1,
    thread_id: "1",
    ordinal: 1,
    audience: "dev",
    question,
    answer: "Because.",
    language: "en",
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    steps: { started_at: 0, ended_at: 1000, steps: steps as any },
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any);
  return { ...t, usage };
}

/** A turn from a server that reports everything: the two details objects, the
 * duration, and a registry that sizes the model. */
const full: Usage = {
  calls: [
    { step: "understand", model: "mimo-v2.5", prompt_tokens: 420, completion_tokens: 90, cached_tokens: 380, ms: 880, cost_usd: 0.0001 },
    { step: "embed", model: "text-embedding-3-small", prompt_tokens: 36, completion_tokens: 0, ms: 190, cost_usd: 0 },
    {
      step: "answer",
      model: "mimo-v2.5-pro",
      prompt_tokens: 22010,
      completion_tokens: 420,
      cached_tokens: 19840,
      ms: 12600,
      context_tokens: 1048576,
      cost_usd: 0.0014,
    },
  ],
  prompt_tokens: 22466,
  completion_tokens: 510,
  total_tokens: 22976,
  cached_tokens: 20220,
  cost_usd: 0.0015,
};

/** A turn from before any of it was recorded: tokens and nothing else. */
const plain: Usage = {
  calls: [
    { step: "understand", model: "mimo-v2.5", prompt_tokens: 400, completion_tokens: 40 },
    { step: "answer", model: "mimo-v2.5-pro", prompt_tokens: 2000, completion_tokens: 500 },
  ],
  prompt_tokens: 2400,
  completion_tokens: 540,
  total_tokens: 2940,
};

const writingStep = {
  step: "writing",
  at: 1000,
  detail: {
    prompt_tokens: 22010,
    completion_tokens: 420,
    cited: 2,
    sources: 6,
    cached_tokens: 19840,
    prompt_system: 3410,
    prompt_sources: 15100,
    prompt_question: 3500,
  },
};
const gatherStep = {
  step: "gathering",
  at: 500,
  detail: { hits: 12, references: 3, crossings: 1, sources: 9, repos: 1, tokens: 14780, budget: 24000 },
};
const searchStep = { step: "searching", at: 300, detail: { hits: 31, per_repo: { rongo: 31 } } };

describe("StatsPane, this turn", () => {
  const rich = () => turnOf("Where is the ceiling checked?", full, [searchStep, gatherStep, writingStep]);

  it("shows what the turn spent, what was cached and how long it took", () => {
    render(<StatsPane target={{ kind: "turn", index: 0 }} turns={[rich()]} onClose={() => {}} />);

    const pane = screen.getByRole("dialog", { name: "Token stats" });
    expect(pane.textContent).toContain("22,976");
    expect(pane.textContent).toContain("$0.001");
    expect(pane.textContent).toContain("20,220"); // served from cache
    expect(pane.textContent).toContain("13.7 s"); // every call, summed
    // The per-call ledger, with the two columns the old table had no room for
    expect(within(pane).getByText("19,840")).toBeTruthy();
    expect(within(pane).getByText("12.6 s")).toBeTruthy();
    // Sub-second durations read in milliseconds
    expect(within(pane).getByText("880 ms")).toBeTruthy();
  });

  it("counts the calls, and the work that cost no tokens separately", () => {
    render(<StatsPane target={{ kind: "turn", index: 0 }} turns={[rich()]} onClose={() => {}} />);

    const pane = screen.getByRole("dialog", { name: "Token stats" });
    expect(pane.textContent).toContain("model calls");
    expect(pane.textContent).toContain("search lanes");
    // 3 references + 1 crossing: the hops the gatherer actually walked.
    expect(pane.textContent).toContain("reference hops");
    expect(within(pane).getByText("4")).toBeTruthy();
  });

  it("gives the window as a number and the prompt split as the bar", () => {
    render(<StatsPane target={{ kind: "turn", index: 0 }} turns={[rich()]} onClose={() => {}} />);

    const pane = screen.getByRole("dialog", { name: "Token stats" });
    expect(pane.textContent).toContain("22,010 of 1,048,576 · 2%");
    expect(pane.textContent).toContain("system prompt");
    expect(pane.textContent).toContain("3,410");
    expect(pane.textContent).toContain("15,100");
    expect(pane.textContent).toContain("will not add up to it exactly");
    // And the budget the gatherer worked to
    expect(pane.textContent).toContain("14,780 of 24,000");
  });

  it("every ⓘ opens one line and closes it again, one at a time", async () => {
    const user = userEvent.setup();
    render(<StatsPane target={{ kind: "turn", index: 0 }} turns={[rich()]} onClose={() => {}} />);

    await user.click(screen.getByRole("button", { name: "What an embedding call means" }));
    expect(screen.getByText(/turned into a vector/)).toBeTruthy();

    // A second one replaces the first rather than stacking.
    await user.click(screen.getByRole("button", { name: "What a search lane means" }));
    expect(screen.queryByText(/turned into a vector/)).toBeNull();
    expect(screen.getByText(/one matches words, one matches meaning/)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "What a reference hop means" }));
    expect(screen.getByText(/pointed at another/)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "What a cached prompt means" }));
    expect(screen.getByText(/did not charge full price/)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "What the context means" }));
    expect(screen.getByText(/comes from the price registry/)).toBeTruthy();

    const budget = screen.getByRole("button", { name: "What the source budget means" });
    await user.click(budget);
    expect(screen.getByText(/the walk stops where it is/)).toBeTruthy();
    await user.click(budget);
    expect(screen.queryByText(/the walk stops where it is/)).toBeNull();
  });

  it("draws nothing it has no figures for", () => {
    render(<StatsPane target={{ kind: "turn", index: 0 }} turns={[turnOf("Old one?", plain)]} onClose={() => {}} />);

    const pane = screen.getByRole("dialog", { name: "Token stats" });
    expect(pane.textContent).toContain("2,940");
    // No duration column, no cache column, no context section, no budget.
    expect(pane.textContent).not.toContain("took");
    expect(pane.textContent).not.toContain("cached");
    expect(pane.textContent).not.toContain("What filled the answer call");
    expect(pane.textContent).not.toContain("Budgets");
    // And no price column on a server with no table.
    expect(pane.textContent).not.toContain("$");
  });

  it("says so when the turn has no usage at all", () => {
    render(<StatsPane target={{ kind: "turn", index: 0 }} turns={[turnOf("Nothing?", null)]} onClose={() => {}} />);
    expect(screen.getByText(/no usage on record/)).toBeTruthy();
  });
});

describe("StatsPane, the thread", () => {
  const turns = [
    turnOf("First?", full, [writingStep]),
    { ...turnOf("Second?", plain), question: "Second?" },
  ];

  it("sums the thread and says where the tokens went", () => {
    render(<StatsPane target={{ kind: "thread" }} turns={turns} onClose={() => {}} />);

    const pane = screen.getByRole("dialog", { name: "Token stats" });
    expect(pane.textContent).toContain("Thread");
    expect(pane.textContent).toContain("25,916"); // 22,976 + 2,940
    expect(pane.textContent).toContain("2 turns");
    // Grouped by step, biggest first: the answer call dominates every thread.
    expect(pane.textContent).toContain("answer");
    expect(pane.textContent).toContain("understand");
    expect(pane.textContent).toContain("Per turn");
    expect(pane.textContent).toContain("Turn 1");
    expect(pane.textContent).toContain("Turn 2");
  });

  it("switches between the turn and the thread", async () => {
    const user = userEvent.setup();
    render(<StatsPane target={{ kind: "turn", index: 0 }} turns={turns} onClose={() => {}} />);

    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Turn 1");
    await user.click(screen.getByRole("button", { name: "Thread", exact: true }));
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Thread");
    await user.click(screen.getByRole("button", { name: "This turn" }));
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Turn 1");
  });

  it("says so when no turn in the thread has usage", () => {
    render(<StatsPane target={{ kind: "thread" }} turns={[turnOf("Nothing?", null)]} onClose={() => {}} />);
    expect(screen.getByText(/no usage on record/)).toBeTruthy();
  });
});

describe("The thread badge", () => {
  it("is a way into the stats where there is one, and a label where there is not", async () => {
    const user = userEvent.setup();
    const onOpen = vi.fn();
    const { rerender } = render(<ThreadUsageBadge total={{ tokens: 25916, cost: 0.013 }} onOpen={onOpen} />);

    const button = screen.getByRole("button", { name: "Thread token stats" });
    expect(button.textContent).toContain("25,916 tok");
    expect(button.textContent).toContain("$0.013");
    await user.click(button);
    expect(onOpen).toHaveBeenCalledTimes(1);

    // The share page passes none: the same figures, nothing to press, because
    // a share carries the total and nothing behind it.
    rerender(<ThreadUsageBadge total={{ tokens: 25916, cost: null }} />);
    expect(screen.queryByRole("button")).toBeNull();
    const label = screen.getByLabelText("Thread usage");
    expect(label.textContent).toContain("25,916 tok");
    expect(label.textContent).not.toContain("$");
  });
});

describe("StatsPane, closing it", () => {
  const turns = [turnOf("First?", full, [writingStep])];

  it("closes on the button, on the scrim and on Escape", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const { container } = render(
      <StatsPane target={{ kind: "turn", index: 0 }} turns={turns} onClose={onClose} />,
    );

    await user.click(screen.getByRole("button", { name: "Close token stats" }));
    expect(onClose).toHaveBeenCalledTimes(1);

    await user.click(container.querySelector(".fixed.inset-0")!);
    expect(onClose).toHaveBeenCalledTimes(2);

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(3);
  });
});
