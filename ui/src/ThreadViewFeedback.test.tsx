import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, within } from "@testing-library/react";
import ThreadView, { type ThreadActions } from "./ThreadView";
import { freshTurn, type Turn } from "./turns";

/**
 * Where the thread's verdict is drawn: among the buttons of the newest
 * answer, owner view only, never while a turn runs.
 */

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

function answered(question: string, id: number): Turn {
  return { ...freshTurn(question, "ba", "en"), text: `About ${question}.`, done: true, live: false, recorded: true, messageId: id };
}

function failed(question: string, id: number): Turn {
  return { ...freshTurn(question, "ba", "en"), error: "boom", done: true, live: false, recorded: true, messageId: id };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, status: 200, json: async () => null })),
  );
}

/** The article a question's answer is drawn in. */
const turnOf = (text: string) => screen.getByText(text).closest("article") as HTMLElement;

describe("ThreadView, the thread's verdict", () => {
  it("sits under the newest answer only", () => {
    stubFetch();
    render(<ThreadView onOpenSource={() => {}} turns={[answered("first", 1), answered("second", 2)]} actions={actions} threadKey="t1" />);

    expect(screen.getAllByRole("button", { name: "Helpful" })).toHaveLength(1);
    expect(within(turnOf("About second.")).getByRole("button", { name: "Helpful" })).toBeTruthy();
  });

  it("stays on the newest answer when a failure follows it", () => {
    stubFetch();
    render(<ThreadView onOpenSource={() => {}} turns={[answered("first", 1), failed("second", 2)]} actions={actions} threadKey="t1" />);

    expect(within(turnOf("About first.")).getByRole("button", { name: "Helpful" })).toBeTruthy();
  });

  it("sits under the answer drawn last, not under a re-explain grouped above it", () => {
    stubFetch();
    const reexplain: Turn = { ...answered("first", 3), text: "About first, again.", headId: 1 };
    render(
      <ThreadView
        onOpenSource={() => {}}
        turns={[answered("first", 1), answered("second", 2), reexplain]}
        actions={actions}
        threadKey="t1"
      />,
    );

    expect(within(turnOf("About second.")).getByRole("button", { name: "Helpful" })).toBeTruthy();
  });

  it("is not drawn while a turn runs", () => {
    stubFetch();
    const running: Turn = { ...freshTurn("second", "ba", "en"), text: "It " };
    render(<ThreadView onOpenSource={() => {}} turns={[answered("first", 1), running]} actions={actions} threadKey="t1" busy />);

    expect(screen.queryByRole("button", { name: "Helpful" })).toBeNull();
  });

  it("is not drawn on a shared page", () => {
    stubFetch();
    render(<ThreadView onOpenSource={() => {}} turns={[answered("first", 1)]} actions={null} threadKey="t1" />);

    expect(screen.queryByRole("button", { name: "Helpful" })).toBeNull();
  });
});
