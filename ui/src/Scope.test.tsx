import { ask, ev, streamFrames } from "./__tests__/helpers";
import { describe, it, expect, vi, afterEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";

afterEach(() => vi.unstubAllGlobals());

describe("the scope notice", () => {
  it("is shown above the answer when a named repository is not indexed", async () => {
    // The search drops an unknown name on purpose, so this sentence is the
    // only thing standing between the reader and an answer about code they
    // did not ask about.
    const notice = 'No repository called "loom" in the index. Answered for "rongo" alone.';
    streamFrames([
      ev("thread", { thread_id: "1", title: "t", message_id: 7 }),
      ev("notice", { text: notice }),
      ev("status", { step: "searching" }),
      ev("token", { text: "rongo keeps no session [1]." }),
      ev("done", { message_id: 7 }),
    ]);

    await ask("How do loom and rongo differ?");

    await waitFor(() => expect(screen.getByText(notice)).toBeTruthy());
    // Above the answer, not inside it: it is about the answer, not part of it.
    const shown = screen.getByText(notice);
    const answer = screen.getByText(/rongo keeps no session/);
    expect(shown.compareDocumentPosition(answer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("says nothing on an ordinary turn", async () => {
    streamFrames([
      ev("thread", { thread_id: "1", title: "t", message_id: 7 }),
      ev("status", { step: "searching" }),
      ev("token", { text: "It works like this [1]." }),
      ev("done", { message_id: 7 }),
    ]);

    await ask("How does indexing work?");

    await waitFor(() => expect(screen.getByText(/It works like this/)).toBeTruthy());
    expect(screen.queryByRole("note")).toBeNull();
  });
});
