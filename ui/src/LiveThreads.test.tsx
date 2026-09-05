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
function liveStream(stored: Record<string, unknown>) {
  const encoder = new TextEncoder();
  const queue: string[] = [];
  let wake: (() => void) | null = null;
  let ended = false;
  const mock = vi.fn(async (url: string, _opts?: RequestInit) => {
    // Keyed by thread: two threads answering with the same record is how a
    // test about the wrong thread being written into passes without meaning
    // anything.
    if (String(url).startsWith("/api/threads/")) {
      const id = String(url).slice("/api/threads/".length);
      return { ok: true, status: 200, json: async () => stored[id] ?? [] };
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
    const stream = liveStream({ 40: other });
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
    const stream = liveStream({ 40: other });
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
    const stream = liveStream({ 40: other });
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
    const stream = liveStream({ 40: other });
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

    // And the next question is filed where the reader is, not where the late
    // id came from. The turn taking the wrong thread id here is worse than a
    // misfiled row: the question would be appended to the thread on screen
    // while the stream wrote into another, so the answer would never appear
    // at all and would only surface on the next reload, in the other thread.
    await stream.push(ev("done", { message_id: 5 }));
    await stream.end();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Question"), "And then?");
    await user.click(screen.getByRole("button", { name: "Ask" }));
    await waitFor(() => {
      const post = stream.mock.mock.calls.find(
        (c) => String(c[0]) === "/api/ask" && String(c[1]?.body).includes("And then?"),
      );
      expect(post).toBeTruthy();
      expect(JSON.parse(String(post![1]?.body)).thread_id).toBe(40);
    });
  });

  // The undo of a failed resume is an index into the thread the card was
  // asked in. Run against another thread it unlocks a card nobody touched
  // there — and the backend answers a second resume of an answered card with
  // 409.
  it("does not undo a failed card into the thread the reader moved to", async () => {
    // Thread 7: an open card, the one the reader answers.
    const open = [
      {
        id: 70,
        ordinal: 0,
        audience: "dev",
        question: "How does indexing decide what to embed?",
        answer: "",
        error: "",
        citations: [],
        clarification: {
          id: 7,
          candidates: [{ idx: 0, title: "The indexer walk", summary: "", repo: "rongo", branch: "master" }],
        },
        from_candidate_idx: -1,
        from_clarification_id: 0,
        created_at: "2026-08-17T10:00:00Z",
      },
    ];
    // Thread 40: a card that WAS answered, and the turn that answered it. It
    // is collapsed to "Chosen: …" and must stay that way — the same turn
    // index as the card above, which is the whole hazard.
    const answered = [
      {
        id: 80,
        ordinal: 0,
        audience: "dev",
        question: "Where is the session cookie set?",
        answer: "",
        error: "",
        citations: [],
        clarification: {
          id: 9,
          candidates: [{ idx: 0, title: "The auth callback", summary: "", repo: "rongo", branch: "master" }],
        },
        from_candidate_idx: -1,
        from_clarification_id: 0,
        created_at: "2026-08-17T10:00:00Z",
      },
      {
        id: 81,
        ordinal: 1,
        audience: "dev",
        question: "Where is the session cookie set?",
        answer: "On the callback that finishes sign-in.",
        error: "",
        citations: [],
        clarification: null,
        from_candidate_idx: 0,
        from_clarification_id: 9,
        head_message_id: 80,
        created_at: "2026-08-17T10:02:00Z",
      },
    ];
    const stream = liveStream({ 7: open, 40: answered });
    const { rerender } = render(<Ask threadId={7} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /The indexer walk/ }));

    rerender(<Ask threadId={40} />);
    await screen.findByText(/Chosen: The auth callback/);

    // The resume fails while they are elsewhere.
    await stream.push(ev("error", { message: "The turn failed." }));
    await stream.end();

    // The card in front of them was answered and stays answered: unlocking it
    // would offer a second resume, which the backend refuses with 409.
    await waitFor(() => expect(screen.queryByText(/Another thread is still being answered/)).toBeNull());
    expect(screen.getByText(/Chosen: The auth callback/)).toBeTruthy();
    // Still folded, too: taking the choice back opens the card again, which
    // is how the reader would be offered the second resume.
    expect(screen.getByRole("button", { name: /Chosen: The auth callback/ }).getAttribute("aria-expanded")).toBe(
      "false",
    );
    expect(screen.queryByText("Which one do you mean?")).toBeNull();
  });
});
