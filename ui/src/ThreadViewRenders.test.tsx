import { describe, it, expect, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";

// The trace of a turn is drawn once per render of that turn, so counting its
// renders per start time says which turns a change re-rendered.
const traceRenders = new Map<number, number>();
vi.mock("./Trace", () => ({
  default: ({ startedAt }: { startedAt: number }) => {
    traceRenders.set(startedAt, (traceRenders.get(startedAt) ?? 0) + 1);
    return null;
  },
}));

import ThreadView, { type ThreadActions } from "./ThreadView";
import { freshTurn, type Turn } from "./turns";

const actions: ThreadActions = {
  onRetry: () => {},
  onReexplain: () => {},
  onCopy: async () => true,
  onCopyQuestion: async () => true,
  onFollowup: () => {},
  onChoose: () => {},
  onNarrow: () => {},
  onOpenStats: () => {},
};

function finished(question: string, startedAt: number): Turn {
  return {
    ...freshTurn(question, "dev", "en"),
    text: `The answer to ${question} [1].`,
    done: true,
    live: false,
    recorded: true,
    messageId: startedAt,
    startedAt,
    endedAt: startedAt + 1000,
    steps: [{ step: "understanding", at: startedAt }],
    citations: [
      { marker: 1, repo: "peeq", branch: "master", path: "a.go", start_line: 1, end_line: 9, sha: "0123abc" },
    ] as Turn["citations"],
  };
}

beforeEach(() => traceRenders.clear());

describe("ThreadView, while an answer streams", () => {
  it("re-renders the turn being written and none of the finished ones", () => {
    // Given a thread of two finished turns and a third being answered
    const one = finished("How is sign-in done?", 1000);
    const two = finished("Where is the token stored?", 2000);
    const running: Turn = { ...freshTurn("And on logout?", "dev", "en"), startedAt: 3000, text: "It " };
    // The caller's handlers are fresh arrows on every render of its own, as
    // Ask's and the share page's are.
    const view = (turns: Turn[]) => (
      <ThreadView
        turns={turns}
        busy
        actions={actions}
        onOpenSource={() => {}}
        onHot={() => {}}
        sourcesOpen
        onToggleSources={() => {}}
        threadKey="t"
      />
    );
    const { rerender, container } = render(view([one, two, running]));
    const before = new Map(traceRenders);

    // When twenty tokens arrive: each replaces the list and the last turn,
    // and leaves the finished turns the objects they were
    let text = running.text;
    for (let n = 0; n < 20; n++) {
      text += "word ";
      rerender(view([one, two, { ...running, text }]));
    }

    // Then the running turn was drawn again for every token, the others not
    // once — a long thread must not be redrawn sixty times a second
    expect(container.textContent).toContain(text.trim());
    expect((traceRenders.get(3000) ?? 0) - (before.get(3000) ?? 0)).toBe(20);
    expect((traceRenders.get(1000) ?? 0) - (before.get(1000) ?? 0)).toBe(0);
    expect((traceRenders.get(2000) ?? 0) - (before.get(2000) ?? 0)).toBe(0);
  });

  it("redraws a finished turn when it is that turn that changed", () => {
    // Given
    const one = finished("How is sign-in done?", 1000);
    const two = finished("Where is the token stored?", 2000);
    const { rerender } = render(
      <ThreadView turns={[one, two]} actions={actions} onOpenSource={() => {}} threadKey="t" />,
    );
    const before = traceRenders.get(1000) ?? 0;

    // When the first turn is replaced — a card answered, a choice taken back
    rerender(
      <ThreadView turns={[{ ...one, notice: "x" }, two]} actions={actions} onOpenSource={() => {}} threadKey="t" />,
    );

    // Then it is drawn again, with what changed
    expect((traceRenders.get(1000) ?? 0) - before).toBe(1);
  });
});
