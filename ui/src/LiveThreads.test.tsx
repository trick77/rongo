import { describe, it, expect, vi, afterEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Ask from "./Ask";

/**
 * A turn a test can write into a frame at a time, plus one stored thread for
 * the reader to open while it is still being written. The whole subject here
 * is what happens BETWEEN the frames, so the stream has to stay open across
 * the assertions rather than arrive complete.
 */
function liveStream(stored: unknown) {
  const encoder = new TextEncoder();
  const queue: string[] = [];
  let wake: (() => void) | null = null;
  let ended = false;
  const mock = vi.fn(async (url: string) => {
    if (String(url).startsWith("/api/threads/")) {
      return { ok: true, status: 200, json: async () => stored };
    }
    return {
      ok: true,
      status: 200,
      body: {
        getReader() {
          return {
            async read() {
              while (queue.length === 0 && !ended) await new Promise<void>((r) => (wake = r));
              const frame = queue.shift();
              if (frame === undefined) return { done: true, value: undefined };
              return { done: false, value: encoder.encode(frame) };
            },
          };
        },
      },
    };
  });
  vi.stubGlobal("fetch", mock);
  const bump = () => {
    const w = wake;
    wake = null;
    w?.();
  };
  return {
    mock,
    async push(frame: string) {
      queue.push(frame);
      await act(async () => bump());
    },
    async end() {
      ended = true;
      await act(async () => bump());
    },
  };
}

const ev = (name: string, data: unknown) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

// The stored conversation the reader opens while the first one is still being
// written.
const other = [
  {
    id: 40,
    ordinal: 0,
    audience: "ba",
    question: "Where is the session cookie set?",
    answer: "On the callback that finishes sign-in.",
    error: "",
    citations: [],
    created_at: "2026-08-17T10:00:00Z",
  },
];

async function askNew() {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Question"), "How does indexing decide what to embed?");
  await user.click(screen.getByRole("button", { name: "Ask" }));
}

describe("Ask, a thread the reader walked away from", () => {
  // The point of letting the rail stay live: an answer is not abandoned by
  // reading something else. It goes on being written where it was asked, and
  // it is all there on the way back — including the part that arrived while
  // the reader was elsewhere.
  it("keeps writing the parked answer and hands it back whole", async () => {
    const stream = liveStream(other);
    const onBusy = vi.fn();
    const { rerender } = render(<Ask threadId={null} onBusy={onBusy} />);
    await askNew();

    await stream.push(ev("thread", { thread_id: 1 }));
    await stream.push(ev("token", { text: "The walker accepts a file, " }));
    await screen.findByText(/The walker accepts a file/);
    // The rail is told WHICH thread is being written, not merely that one is.
    expect(onBusy).toHaveBeenCalledWith(true, 1);

    // The reader opens another thread. The record answers for that one; the
    // half-written answer leaves the screen with the thread it belongs to.
    rerender(<Ask threadId={40} onBusy={onBusy} />);
    await screen.findByText(/On the callback that finishes sign-in/);
    expect(screen.queryByText(/The walker accepts/)).toBeNull();

    // A token landing while they read elsewhere goes to the parked turn and
    // never into the conversation in front of them.
    await stream.push(ev("token", { text: "then the splitter cuts it." }));
    expect(screen.queryByText(/splitter/)).toBeNull();

    rerender(<Ask threadId={1} onBusy={onBusy} />);
    // Both halves: the one they watched arrive and the one that landed while
    // they were reading somewhere else. (Two text nodes — the streamed answer
    // is rendered a segment at a time.)
    await screen.findByText(/then the splitter cuts it/);
    expect(screen.getAllByText(/The walker accepts a file/).length).toBeGreaterThan(0);
    // Restored from the parked copy, never fetched: the record carries no
    // answer until the turn ends, so a fetch would have shown an empty thread.
    expect(stream.mock.mock.calls.some((c) => String(c[0]) === "/api/threads/1")).toBe(false);
  });

  // The suggestions arrive after the answer. A reader who was elsewhere when
  // they landed must still find them under the answer they belong to.
  it("brings the suggestions back with the answer", async () => {
    const stream = liveStream(other);
    const { rerender } = render(<Ask threadId={null} />);
    await askNew();
    await stream.push(ev("thread", { thread_id: 1 }));
    await stream.push(ev("token", { text: "Chunks reach the embedder." }));
    await screen.findByText(/Chunks reach the embedder/);

    rerender(<Ask threadId={40} />);
    await screen.findByText(/On the callback that finishes sign-in/);
    await stream.push(ev("citations", []));
    await stream.push(ev("followups", ["What is the size ceiling?"]));
    await stream.push(ev("done", { message_id: 5 }));

    rerender(<Ask threadId={1} />);
    expect(await screen.findByRole("button", { name: "What is the size ceiling?" })).toBeTruthy();
  });

  // A dimmed Ask button whose reason lives in another conversation explains
  // nothing at all. This is the line that explains it.
  it("says why the composer will not take a question", async () => {
    const stream = liveStream(other);
    const { rerender } = render(<Ask threadId={null} />);
    await askNew();
    await stream.push(ev("thread", { thread_id: 1 }));
    await stream.push(ev("token", { text: "Still writing…" }));
    await screen.findByText(/Still writing/);
    // Not in the thread being written: the running turn is right there.
    expect(screen.queryByText(/Another thread is still being answered/)).toBeNull();

    rerender(<Ask threadId={40} />);
    await screen.findByText(/On the callback that finishes sign-in/);
    expect(screen.getByText(/Another thread is still being answered/)).toBeTruthy();

    await stream.push(ev("done", { message_id: 5 }));
    await stream.end();
    await waitFor(() => expect(screen.queryByText(/Another thread is still being answered/)).toBeNull());
  });

  // The thread id arrives after the question was sent. A reader who has moved
  // on by then must not be dragged back to the thread they left.
  it("does not pull the reader back when the thread id arrives late", async () => {
    const stream = liveStream(other);
    const onThread = vi.fn();
    const { rerender } = render(<Ask threadId={null} onThread={onThread} />);
    await askNew();

    rerender(<Ask threadId={40} onThread={onThread} />);
    await screen.findByText(/On the callback that finishes sign-in/);

    await stream.push(ev("thread", { thread_id: 1 }));
    await stream.push(ev("token", { text: "Still writing…" }));

    expect(onThread).not.toHaveBeenCalledWith(1);
    expect(screen.queryByText(/Still writing/)).toBeNull();
    expect(screen.getByText(/On the callback that finishes sign-in/)).toBeTruthy();
  });
});
