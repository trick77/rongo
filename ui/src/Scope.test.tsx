import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Ask from "./Ask";

function streamFrames(frames: string[]) {
  const encoder = new TextEncoder();
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: true,
      status: 200,
      body: {
        getReader() {
          let i = 0;
          return {
            async read() {
              if (i >= frames.length) return { done: true, value: undefined };
              return { done: false, value: encoder.encode(frames[i++]) };
            },
          };
        },
      },
    })),
  );
}

const ev = (name: string, data: unknown) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;

afterEach(() => vi.unstubAllGlobals());

async function ask(text: string) {
  const user = userEvent.setup();
  render(
    <StrictMode>
      <Ask />
    </StrictMode>,
  );
  await user.type(screen.getByLabelText("Question"), text);
  await user.click(screen.getByRole("button", { name: "Ask" }));
  return user;
}

describe("the scope notice", () => {
  it("is shown above the answer when a named repository is not indexed", async () => {
    // The search drops an unknown name on purpose, so this sentence is the
    // only thing standing between the reader and an answer about code they
    // did not ask about.
    const notice = 'No repository called "loom" in the index. Answered for "rongo" alone.';
    streamFrames([
      ev("thread", { thread_id: 1, title: "t", message_id: 7 }),
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
      ev("thread", { thread_id: 1, title: "t", message_id: 7 }),
      ev("status", { step: "searching" }),
      ev("token", { text: "It works like this [1]." }),
      ev("done", { message_id: 7 }),
    ]);

    await ask("How does indexing work?");

    await waitFor(() => expect(screen.getByText(/It works like this/)).toBeTruthy());
    expect(screen.queryByRole("note")).toBeNull();
  });
});
