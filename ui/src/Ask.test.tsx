import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Ask from "./Ask";

/**
 * Streams the given SSE frames one chunk at a time. A fake that returned the
 * whole body at once would let a component that waits for the end pass, and the
 * symptom in the real app is an answer that appears only when it is finished.
 */
function streamFrames(frames: string[], status = 200) {
  const encoder = new TextEncoder();
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: status >= 200 && status < 300,
      status,
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

afterEach(() => {
  vi.unstubAllGlobals();
  // The composer's language survives a reload now, so it survives a test too.
  localStorage.clear();
});

// Mounted under StrictMode, like every test in this file and like main.tsx
// mounts the real app: StrictMode runs each effect twice, and a component
// that only tolerates a single run passes here while coming back empty on a
// real reload.
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

describe("Ask", () => {
  it("greets in the answer language", async () => {
    // The select says Deutsch and the page still says "Ask about the code."
    render(<Ask />);
    expect(screen.getByRole("heading", { name: "Ask about the code." })).toBeTruthy();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Answer language"), "de");
    expect(screen.getByRole("heading", { name: "Frag den Code." })).toBeTruthy();
    expect(screen.queryByText(/Ask about the code/)).toBeNull();
    await user.selectOptions(screen.getByLabelText("Answer language"), "en");
    expect(screen.getByRole("heading", { name: "Ask about the code." })).toBeTruthy();
  });

  // The composer sits next to the language select and is the place the answer
  // is asked for, so an English invitation under a German setting is the one
  // piece of chrome that reads as a bug rather than as a convention.
  it("invites the question in the answer language", async () => {
    render(<Ask />);
    const box = screen.getByLabelText("Question");
    expect(box.getAttribute("placeholder")).toBe("Ask about the code…");
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Answer language"), "de");
    expect(box.getAttribute("placeholder")).toBe("Frag den Code …");
  });

  it("shows the answer as it arrives, not only at the end", async () => {
    streamFrames([
      ev("thread", { thread_id: 1, title: "x" }),
      ev("token", { text: "Shipping " }),
      ev("token", { text: "runs through a job [1]." }),
      ev("citations", [
        { marker: 1, repo: "peeq", branch: "master", path: "a.go", start_line: 1, end_line: 9 },
      ]),
      ev("done", { message_id: 1 }),
    ]);

    await ask("How does shipping work?");

    await screen.findByText(/Shipping runs through a job/);
  });

  it("shows the running step while nothing is finished", async () => {
    streamFrames([ev("thread", { thread_id: 1 }), ev("status", { step: "gathering" })]);

    await ask("How?");

    await waitFor(() => {
      expect(screen.getByRole("status").textContent).toContain("Reading the code");
    });
  });

  it("lists the sources with their branch - without it a forge link leads nowhere", async () => {
    streamFrames([
      ev("thread", { thread_id: 1 }),
      ev("token", { text: "So ist es [1]." }),
      ev("citations", [
        {
          marker: 1,
          repo: "peeq",
          branch: "release-2024.3",
          path: "backend/internal/playbackgrant/store.go",
          start_line: 3,
          end_line: 40,
        },
      ]),
      ev("done", {}),
    ]);

    await ask("How?");

    const evidence = await screen.findByText(/How does rongo know this/);
    expect(evidence).toBeTruthy();
    const turn = evidence.closest("article")!;
    expect(turn.textContent).toContain("release-2024.3");
    expect(turn.textContent).toContain("store.go:3-40");
    // The Sources pane lists the same file, so a reader keeps it in view.
    expect(screen.getByRole("complementary", { name: "Sources" }).textContent).toContain("store.go");
  });

  it("opens the cited file, at the cited commit, when a source is clicked", async () => {
    streamFrames([
      ev("thread", { thread_id: 1 }),
      ev("token", { text: "So [1]." }),
      ev("citations", [
        { marker: 1, repo: "peeq", branch: "master", path: "internal/a.go", start_line: 2, end_line: 3, sha: "0123abcdef" },
      ]),
      ev("done", {}),
    ]);
    const user = await ask("How?");
    await screen.findByText(/How does rongo know this/);

    // From here on fetch serves the file, not the stream.
    const fetchMock = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ repo: "peeq", branch: "master", path: "internal/a.go", sha: "0123abcdef", content: "a\nb\nc\n" }),
      text: async () => "",
    }));
    vi.stubGlobal("fetch", fetchMock);

    const pane = screen.getByRole("complementary", { name: "Sources" });
    await user.click(pane.querySelector("button")!);

    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("a.go");
    expect(String((fetchMock.mock.calls[0] as unknown[])[0])).toBe(
      "/api/source?repo=peeq&path=internal%2Fa.go&sha=0123abcdef",
    );
    await screen.findByText("b");
    expect(Array.from(dialog.querySelectorAll("[data-hit]")).map((h) => h.getAttribute("data-line"))).toEqual(["2", "3"]);

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("shows an error as an error, not as an empty answer", async () => {
    streamFrames([ev("thread", { thread_id: 1 }), ev("error", { message: "The turn failed." })]);

    await ask("How?");

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("failed");
    });
  });

  it("sends the chosen role along", async () => {
    streamFrames([ev("thread", { thread_id: 1 }), ev("done", {})]);
    const user = userEvent.setup();
    render(<Ask />);
    await user.click(screen.getByRole("button", { name: "Developer" }));
    await user.type(screen.getByLabelText("Question"), "How?");
    await user.click(screen.getByRole("button", { name: "Ask" }));

    await waitFor(() => {
      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body);
      expect(body.audience).toBe("dev");
    });
  });

  it("sends the chosen answer language along", async () => {
    streamFrames([ev("thread", { thread_id: 1 }), ev("done", {})]);
    const user = userEvent.setup();
    render(<Ask />);
    await user.selectOptions(screen.getByLabelText("Answer language"), "de");
    await user.type(screen.getByLabelText("Question"), "Wie?");
    await user.click(screen.getByRole("button", { name: "Ask" }));

    await waitFor(() => {
      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body);
      expect(body.language).toBe("de");
    });
  });

  it("asks on Enter and keeps Shift+Enter for a new line", async () => {
    streamFrames([ev("thread", { thread_id: 1 }), ev("done", {})]);
    const user = userEvent.setup();
    render(<Ask />);
    await user.type(screen.getByLabelText("Question"), "First line{Shift>}{Enter}{/Shift}second");
    expect(globalThis.fetch).not.toHaveBeenCalled();
    await user.type(screen.getByLabelText("Question"), "{Enter}");
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1));
    const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body);
    expect(body.question).toBe("First line\nsecond");
  });

  it("appends the second question to the same thread", async () => {
    // The thread is a record: a follow-up continues it rather than starting a
    // second conversation about the same subject.
    streamFrames([ev("thread", { thread_id: 42 }), ev("done", {})]);
    const user = await ask("First question?");

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1));
    streamFrames([ev("thread", { thread_id: 42 }), ev("done", {})]);
    await user.type(screen.getByLabelText("Question"), "And then?");
    await user.click(screen.getByRole("button", { name: "Ask" }));

    await waitFor(() => {
      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body);
      expect(body.thread_id).toBe(42);
    });
  });
});

/**
 * Routes by URL, because Ask now talks to two endpoints: it streams from
 * /api/ask and reads a stored thread from /api/threads/{id}. A stub that
 * answered both the same way would let a component that confuses them pass.
 */
function routedFetch(messages: unknown, frames: string[] = []) {
  const encoder = new TextEncoder();
  const mock = vi.fn(async (url: string, _opts?: RequestInit) => {
    if (String(url).startsWith("/api/threads/")) {
      return { ok: true, status: 200, json: async () => messages };
    }
    return {
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
    };
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

const storedTurn = {
  id: 9,
  ordinal: 0,
  audience: "dev",
  question: "How does an Apple TV get at the file?",
  answer: "Through a grant [1].",
  error: "",
  citations: [
    {
      marker: 1,
      repo: "peeq",
      branch: "master",
      path: "backend/internal/playbackgrant/store.go",
      start_line: 3,
      end_line: 40,
    },
  ],
  created_at: "2026-08-17T10:00:00Z",
};

describe("Ask, a stored thread", () => {
  // Rendered the way main.tsx mounts the app. StrictMode runs every effect
  // twice, and the first version of the loader cancelled its own only request
  // in the cleanup between the two runs: the tests passed, the real app came
  // back from a reload with an empty thread.
  const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

  it("restores an old turn including its sources from the record", async () => {
    routedFetch([storedTurn]);
    strict(<Ask threadId={7} />);

    expect(await screen.findByText(/Through a grant/)).toBeTruthy();
    expect(screen.getByText(/How does an Apple TV get at the file/)).toBeTruthy();
    expect(screen.getByText(/store\.go:3-40/).closest("article")).toBeTruthy();
    // A restored turn is finished. A status line would claim something is
    // still running.
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("opens a source from a chip in an older turn, not only from the pane", async () => {
    // On a tablet the pane is not there and hover does not exist. The chip
    // is the way to the source, in every turn, from that turn's own list.
    const newer = {
      ...storedTurn,
      id: 10,
      ordinal: 1,
      question: "And the token?",
      answer: "From the header [1].",
      citations: [{ ...storedTurn.citations[0], path: "backend/internal/httpapi/token.go" }],
    };
    routedFetch([storedTurn, newer]);
    strict(<Ask threadId={7} />);
    await screen.findByText(/From the header/);

    const fetchMock = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ repo: "peeq", branch: "master", path: "backend/internal/playbackgrant/store.go", sha: "", content: "a\n" }),
      text: async () => "",
    }));
    vi.stubGlobal("fetch", fetchMock);

    const older = screen.getByText(/Through a grant/).closest("article")!;
    const user = userEvent.setup();
    await user.click(older.querySelector("sup button")!);

    const dialog = await screen.findByRole("dialog");
    expect(dialog.getAttribute("aria-label")).toContain("playbackgrant/store.go");
    expect(String((fetchMock.mock.calls[0] as unknown[])[0])).toContain("playbackgrant%2Fstore.go");
  });

  it("restores the role the question was answered in", async () => {
    routedFetch([storedTurn]);
    strict(<Ask threadId={7} />);
    // The eyebrow over the question, not the composer's toggle button.
    await screen.findByText(/Through a grant/);
    expect(screen.getAllByText("Developer").some((el) => el.tagName !== "BUTTON")).toBe(true);
  });

  // Messages() puts the subject inside the WHERE clause and returns an empty
  // list for a thread that is not yours or no longer exists — 200, not 403.
  // Waiting for an error status would leave a dead id in localStorage forever.
  it("hands back a dead thread id instead of showing an empty thread", async () => {
    routedFetch([]);
    const onThread = vi.fn();
    strict(<Ask threadId={999} onThread={onThread} />);
    await waitFor(() => expect(onThread).toHaveBeenCalledWith(null));
  });

  it("does not reload its own running thread mid-stream", async () => {
    // The stream's thread event reports the id back upwards; if that round trip
    // re-triggered the loader, the half-written answer would be replaced by the
    // stored record, which does not have it yet.
    const mock = routedFetch(
      [storedTurn],
      [ev("thread", { thread_id: 42 }), ev("token", { text: "Shipping runs." }), ev("done", {})],
    );
    const user = userEvent.setup();
    const { rerender } = render(<Ask threadId={null} onThread={() => {}} />);
    await user.type(screen.getByLabelText("Question"), "How?");
    await user.click(screen.getByRole("button", { name: "Ask" }));
    await screen.findByText(/Shipping runs/);

    rerender(<Ask threadId={42} onThread={() => {}} />);
    await waitFor(() =>
      expect(mock.mock.calls.filter((c) => String(c[0]).startsWith("/api/threads/")).length).toBe(0),
    );
    expect(screen.getByText(/Shipping runs/)).toBeTruthy();
  });

  /**
   * Like routedFetch, but the thread load only resolves when the test releases
   * it. Every bug below lives in the window between sending that request and
   * its answer arriving, and a fake that resolves immediately closes exactly
   * that window.
   */
  function slowThreadFetch(messages: unknown, frames: string[] = [], status = 200) {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const encoder = new TextEncoder();
    const mock = vi.fn(async (url: string) => {
      if (String(url).startsWith("/api/threads/")) {
        await gate;
        return { ok: status >= 200 && status < 300, status, json: async () => messages };
      }
      return {
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
      };
    });
    vi.stubGlobal("fetch", mock);
    return { mock, release: () => release() };
  }

  it("does not put a deselected thread on screen after the fact", async () => {
    // Pick a thread, then «New question» before the answer is there. The
    // arriving answer belongs to a thread that is no longer open.
    const { release } = slowThreadFetch([storedTurn]);
    const { rerender } = render(<Ask threadId={7} onThread={() => {}} />);
    rerender(<Ask threadId={null} onThread={() => {}} />);
    // Released and then flushed: a plain waitFor can poll before the resolved
    // promise's continuation has run and pass on a component that is about to
    // render the wrong thread.
    release();
    await act(async () => {});
    expect(screen.queryByText(/Through a grant/)).toBeNull();
  });

  it("does not overwrite a running turn with the stored record", async () => {
    // A reload onto a remembered thread while the backend is slow: the question
    // is already sent when the record arrives. Without a guard the running turn
    // is dropped and the remaining tokens land in a finished, stored answer.
    const { release } = slowThreadFetch([storedTurn], [
      ev("thread", { thread_id: 7 }),
      ev("token", { text: "The new answer." }),
      ev("done", {}),
    ]);
    const user = userEvent.setup();
    render(<Ask threadId={7} onThread={() => {}} />);
    await user.type(screen.getByLabelText("Question"), "And now?");
    await user.click(screen.getByRole("button", { name: "Ask" }));
    await screen.findByText(/The new answer/);

    release();
    await act(async () => {});
    expect(screen.getByText(/The new answer/)).toBeTruthy();
    expect(screen.getByText("And now?")).toBeTruthy();
  });

  it("does not forget the thread when the server stumbles briefly", async () => {
    // 503 means «not right now», not «does not exist». Only a 200 with an empty
    // list means the thread is not yours or is gone.
    const { release } = slowThreadFetch(null, [], 503);
    const onThread = vi.fn();
    render(<Ask threadId={7} onThread={onThread} />);
    release();
    await act(async () => {});
    expect(onThread).not.toHaveBeenCalledWith(null);
  });

  it("does not keep showing the old thread after a failed switch", async () => {
    // Switch from thread 7 to 8 while the network drops. If thread 7 stays on
    // screen, the next question silently goes into thread 8.
    routedFetch([storedTurn]);
    const { rerender } = render(<Ask threadId={7} onThread={() => {}} />);
    await screen.findByText(/Through a grant/);

    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("offline");
      }),
    );
    rerender(<Ask threadId={8} onThread={() => {}} />);
    await waitFor(() => expect(screen.queryByText(/Through a grant/)).toBeNull());
  });

  it("reports the thread upwards as soon as it exists, and when the turn is done", async () => {
    routedFetch([], [ev("thread", { thread_id: 42 }), ev("token", { text: "So." }), ev("done", {})]);
    const onActivity = vi.fn();
    const user = userEvent.setup();
    render(<Ask threadId={null} onActivity={onActivity} />);
    await user.type(screen.getByLabelText("Question"), "How?");
    await user.click(screen.getByRole("button", { name: "Ask" }));

    // Twice: once so the placeholder title appears immediately, once at the end
    // so the model-written title replaces it.
    await waitFor(() => expect(onActivity).toHaveBeenCalledTimes(2));
  });
});

/**
 * Answers several POSTs in a row, one SSE stream per call, while routing
 * /api/threads/{id} to a fixed stored record. Choosing a candidate and
 * re-explaining both post a SECOND time in the same test, and the queue is
 * what tells the two calls apart.
 */
function queuedPostFetch(responses: string[][], threadsJson: unknown = []) {
  const encoder = new TextEncoder();
  let next = 0;
  const mock = vi.fn(async (url: string, _opts?: RequestInit) => {
    if (String(url).startsWith("/api/threads/")) {
      return { ok: true, status: 200, json: async () => threadsJson };
    }
    const frames = responses[next++] ?? [];
    return {
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
    };
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

describe("Ask, what a turn cost", () => {
  const usage = {
    calls: [
      { step: "understand", model: "mimo-v2.5", prompt_tokens: 400, completion_tokens: 40 },
      { step: "embed", model: "text-embedding-3-small", prompt_tokens: 12, completion_tokens: 0 },
      { step: "answer", model: "mimo-v2.5-pro", prompt_tokens: 2000, completion_tokens: 500 },
    ],
    prompt_tokens: 2412,
    completion_tokens: 540,
    total_tokens: 2952,
  };

  it("shows the turn's tokens and opens the per-call breakdown on the pill", async () => {
    streamFrames([
      ev("thread", { thread_id: 1 }),
      ev("token", { text: "The answer." }),
      ev("citations", []),
      ev("usage", usage),
      ev("done", { message_id: 1 }),
    ]);

    const user = await ask("How?");

    // Tokens only: no price is configured, so no money anywhere.
    const pill = await screen.findByRole("button", { name: "Usage of turn 1" });
    expect(pill.textContent).toContain("2,952 tok");
    expect(pill.textContent).not.toContain("$");
    expect(screen.queryByText("understand")).toBeNull();

    await user.click(pill);

    // Every call the turn made, the gates included, with its deployment.
    expect(screen.getByText("understand")).toBeTruthy();
    expect(screen.getByText("mimo-v2.5-pro")).toBeTruthy();
    expect(screen.getByText("embed")).toBeTruthy();
    expect(screen.getByText(/no price table is loaded/)).toBeTruthy();
  });

  it("shows money once the server prices the calls", async () => {
    streamFrames([
      ev("thread", { thread_id: 1 }),
      ev("token", { text: "The answer." }),
      ev("usage", { ...usage, cost_usd: 0.0081 }),
      ev("done", { message_id: 1 }),
    ]);

    await ask("How?");

    const pill = await screen.findByRole("button", { name: "Usage of turn 1" });
    expect(pill.textContent).toContain("$0.008");
  });

  it("keeps the pill on a turn that asked back or failed - the gates were paid for", async () => {
    streamFrames([
      ev("thread", { thread_id: 1 }),
      ev("usage", { ...usage, calls: usage.calls.slice(0, 2), total_tokens: 452 }),
      ev("error", { message: "The turn failed." }),
    ]);

    await ask("How?");

    const pill = await screen.findByRole("button", { name: "Usage of turn 1" });
    expect(pill.textContent).toContain("452 tok");
    // But no actions: there is no answer to re-explain or copy.
    expect(screen.queryByRole("button", { name: /Explain as/ })).toBeNull();
  });

  it("reports the thread's running total upwards, every turn summed", async () => {
    const onUsage = vi.fn();
    streamFrames([
      ev("thread", { thread_id: 1 }),
      ev("token", { text: "The answer." }),
      ev("usage", { ...usage, cost_usd: 0.01 }),
      ev("done", { message_id: 1 }),
    ]);
    const user = userEvent.setup();
    render(
      <StrictMode>
        <Ask onUsage={onUsage} />
      </StrictMode>,
    );
    await user.type(screen.getByLabelText("Question"), "How?");
    await user.click(screen.getByRole("button", { name: "Ask" }));

    await waitFor(() => expect(onUsage).toHaveBeenLastCalledWith({ tokens: 2952, cost: 0.01 }));

    // A second turn adds to it.
    streamFrames([
      ev("thread", { thread_id: 1 }),
      ev("token", { text: "More." }),
      ev("usage", { ...usage, cost_usd: 0.02 }),
      ev("done", { message_id: 2 }),
    ]);
    await user.type(screen.getByLabelText("Question"), "And then?");
    await user.click(screen.getByRole("button", { name: "Ask" }));
    await waitFor(() => expect(onUsage).toHaveBeenLastCalledWith({ tokens: 5904, cost: 0.03 }));
  });

  it("restores a stored turn's usage from the record", async () => {
    const onUsage = vi.fn();
    routedFetch([{ ...storedTurn, usage: { ...usage, cost_usd: 0.005 } }]);
    render(
      <StrictMode>
        <Ask threadId={7} onUsage={onUsage} />
      </StrictMode>,
    );

    const pill = await screen.findByRole("button", { name: "Usage of turn 1" });
    expect(pill.textContent).toContain("2,952 tok");
    expect(pill.textContent).toContain("$0.005");
    await waitFor(() => expect(onUsage).toHaveBeenLastCalledWith({ tokens: 2952, cost: 0.005 }));
  });
});

describe("Ask, the clarification and re-explaining", () => {
  const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

  const loginCandidate = {
    idx: 0,
    title: "Through the login service",
    summary: "Sign-in runs through the central login service.",
    repo: "peeq",
    branch: "master",
  };
  const legacyCandidate = {
    idx: 1,
    title: "Through the legacy adapter",
    summary: "The old adapter signs users in straight against LDAP.",
    repo: "peeq-legacy",
    branch: "master",
  };

  it("renders the card on a clarification and ends the turn's trace in the waiting state", async () => {
    queuedPostFetch([
      [
        ev("thread", { thread_id: 1, message_id: 5 }),
        ev("status", { step: "understanding" }),
        ev("clarification", { message_id: 5, candidates: [loginCandidate, legacyCandidate] }),
        ev("done", { message_id: 5 }),
      ],
    ]);

    await ask("How is sign-in done?");

    expect(await screen.findByText("Which one do you mean?")).toBeTruthy();
    expect(screen.getByText("Through the login service")).toBeTruthy();
    expect(screen.getByText("Through the legacy adapter")).toBeTruthy();
    // The waiting node, not the check: a person is being waited on.
    expect(screen.getByRole("status").textContent).toContain("Waiting for a choice");
    expect(screen.getByRole("status").textContent).not.toContain("Done");
  });

  it("sends the choice when a candidate is picked and streams the answer into a new turn", async () => {
    const mock = queuedPostFetch([
      [
        ev("thread", { thread_id: 7, message_id: 5 }),
        ev("clarification", { message_id: 5, candidates: [loginCandidate, legacyCandidate] }),
        ev("done", { message_id: 5 }),
      ],
      [
        ev("thread", { thread_id: 7, message_id: 6 }),
        ev("token", { text: "Sign-in runs through the login service." }),
        ev("done", { message_id: 6 }),
      ],
    ]);

    const user = await ask("How is sign-in done?");
    await screen.findByText("Through the login service");

    await user.click(screen.getByText("Through the login service"));

    // The answer's prose is cut into segments for the streaming fade, so the
    // sentence spans several elements and only the paragraph holds it whole.
    await screen.findByText(
      (_, el) => el?.tagName === "P" && (el.textContent ?? "").includes("Sign-in runs through the login service"),
    );
    // The card itself is marked, not overwritten — it still shows both
    // candidates when reopened, the chosen one included.
    expect(await screen.findByText(/Chosen: Through the login service/)).toBeTruthy();

    const postBodies = mock.mock.calls
      .filter((c) => c[1]?.method === "POST")
      .map((c) => JSON.parse(String(c[1]?.body)));
    expect(postBodies[1]).toMatchObject({
      thread_id: 7,
      clarification_message_id: 5,
      choice: 0,
    });
  });

  it("sends nothing when an answered card is clicked again", async () => {
    // One card, one answer: a second choice would be a second answer to a
    // question already decided, and the backend refuses it with 409 anyway.
    const mock = queuedPostFetch([
      [
        ev("thread", { thread_id: 7, message_id: 5 }),
        ev("clarification", { message_id: 5, candidates: [loginCandidate, legacyCandidate] }),
        ev("done", { message_id: 5 }),
      ],
      [
        ev("thread", { thread_id: 7, message_id: 6 }),
        ev("token", { text: "Sign-in runs through the login service." }),
        ev("done", { message_id: 6 }),
      ],
    ]);

    const user = await ask("How is sign-in done?");
    await screen.findByText("Through the login service");
    await user.click(screen.getByText("Through the login service"));
    await screen.findByText(/Chosen: Through the login service/);

    // Reopened, the card is a record: neither candidate posts anything.
    await user.click(screen.getByRole("button", { name: /Chosen/ }));
    await user.click(screen.getByText("Through the legacy adapter"));
    await user.click(screen.getByText("Through the login service"));

    const posts = mock.mock.calls.filter((c) => c[1]?.method === "POST");
    expect(posts.length).toBe(2);
  });

  it("unlocks the card again when the resumed turn fails", async () => {
    // The choice is recorded by the answer, so a turn that never produced one
    // leaves the card open on the server. A card locked in the browser would
    // strand the reader with no way to retry.
    queuedPostFetch([
      [
        ev("thread", { thread_id: 7, message_id: 5 }),
        ev("clarification", { message_id: 5, candidates: [loginCandidate, legacyCandidate] }),
        ev("done", { message_id: 5 }),
      ],
      [ev("thread", { thread_id: 7, message_id: 6 }), ev("error", { message: "The turn failed." })],
    ]);

    const user = await ask("How is sign-in done?");
    await screen.findByText("Through the login service");
    await user.click(screen.getByText("Through the login service"));

    expect(await screen.findByText("Which one do you mean?")).toBeTruthy();
    expect(screen.getByText("Through the legacy adapter").closest("button")?.hasAttribute("disabled")).toBe(false);
  });

  it("unlocks the card again when the server refuses the choice", async () => {
    // The card may have been answered in another tab. The refusal must put the
    // card back the way it was, not leave it locked on a choice that never
    // happened.
    const encoder = new TextEncoder();
    const frames = [
      ev("thread", { thread_id: 7, message_id: 5 }),
      ev("clarification", { message_id: 5, candidates: [loginCandidate, legacyCandidate] }),
      ev("done", { message_id: 5 }),
    ];
    let posts = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        if (String(url).startsWith("/api/threads/")) return { ok: true, status: 200, json: async () => [] };
        if (posts++ > 0) return { ok: false, status: 409 };
        let i = 0;
        return {
          ok: true,
          status: 200,
          body: {
            getReader: () => ({
              async read() {
                if (i >= frames.length) return { done: true, value: undefined };
                return { done: false, value: encoder.encode(frames[i++]) };
              },
            }),
          },
        };
      }),
    );

    const user = await ask("How is sign-in done?");
    await screen.findByText("Through the login service");
    await user.click(screen.getByText("Through the login service"));

    expect(await screen.findByText("Which one do you mean?")).toBeTruthy();
    expect(screen.getByText("Through the login service").closest("button")?.hasAttribute("disabled")).toBe(false);
  });

  const clarifyingMessage = {
    id: 5,
    ordinal: 0,
    audience: "ba",
    question: "How is sign-in done?",
    answer: "",
    error: "",
    citations: [],
    clarification: { id: 50, candidates: [loginCandidate, legacyCandidate] },
    from_candidate_idx: -1,
    from_clarification_id: 0,
    created_at: "2026-08-17T10:00:00Z",
  };
  const resumedMessage = {
    id: 6,
    ordinal: 1,
    audience: "ba",
    question: "How is sign-in done?",
    answer: "Sign-in runs through the login service.",
    error: "",
    citations: [],
    clarification: null,
    from_candidate_idx: 0,
    from_clarification_id: 50,
    created_at: "2026-08-17T10:01:00Z",
  };

  it("renders a stored card collapsed after a reload", async () => {
    // GET /api/threads/{id} carries the clarification; without this a reload
    // shows a turn that looks stuck forever.
    routedFetch([clarifyingMessage, resumedMessage]);
    strict(<Ask threadId={7} />);

    expect(await screen.findByText(/Chosen: Through the login service/)).toBeTruthy();
    expect(screen.queryByText("Which one do you mean?")).toBeNull();
    // A restored turn carries no live trace.
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("marks the right card with two clarifications open, even when the OLDER one is resolved last", async () => {
    // c1 opens, c2 opens, then r1 resolves c1 — the OLDER one, resolved
    // SECOND. A heuristic that reads "the next message" would look at c2 for
    // c1's answer and never find it, leaving c1 stuck open forever, and would
    // never notice c2 was left open at all.
    const c1 = {
      id: 10,
      ordinal: 0,
      audience: "ba",
      question: "How is sign-in done?",
      answer: "",
      error: "",
      citations: [],
      clarification: { id: 100, candidates: [loginCandidate, legacyCandidate] },
      from_candidate_idx: -1,
      from_clarification_id: 0,
      created_at: "2026-08-17T10:00:00Z",
    };
    const c2 = {
      id: 11,
      ordinal: 1,
      audience: "ba",
      question: "How is the invoice created?",
      answer: "",
      error: "",
      citations: [],
      clarification: {
        id: 101,
        candidates: [
          { idx: 0, title: "Through the billing job", summary: "A batch job.", repo: "peeq", branch: "master" },
          { idx: 1, title: "Through checkout", summary: "Directly at checkout.", repo: "peeq", branch: "master" },
        ],
      },
      from_candidate_idx: -1,
      from_clarification_id: 0,
      created_at: "2026-08-17T10:01:00Z",
    };
    const r1 = {
      id: 12,
      ordinal: 2,
      audience: "ba",
      question: "How is sign-in done?",
      answer: "Sign-in runs through the login service.",
      error: "",
      citations: [],
      clarification: null,
      from_candidate_idx: 0,
      from_clarification_id: 100,
      created_at: "2026-08-17T10:02:00Z",
    };
    routedFetch([c1, c2, r1]);
    strict(<Ask threadId={7} />);

    // c1's card is collapsed and marked with the choice r1 recorded.
    expect(await screen.findByText(/Chosen: Through the login service/)).toBeTruthy();
    // c2 was never resolved, so its card is still open, asking.
    expect(screen.getByText("Which one do you mean?")).toBeTruthy();
    expect(screen.getByText("Through the billing job")).toBeTruthy();
  });

  it("posts a re-explain to the reexplain route and never to /api/ask", async () => {
    const mock = routedFetch([storedTurn], [
      ev("thread", { thread_id: 3, message_id: 20 }),
      ev("token", { text: "An answer for the BA." }),
      ev("done", { message_id: 20 }),
    ]);
    strict(<Ask threadId={7} />);
    await screen.findByText(/Through a grant/);

    const user = userEvent.setup();
    // storedTurn is audience "dev", so the button offers the Analyst re-explain.
    await user.click(screen.getByRole("button", { name: "Explain as Analyst" }));

    await screen.findByText(/An answer for the BA/);

    const postCalls = mock.mock.calls.filter((c) => c[1]?.method === "POST");
    expect(postCalls.length).toBe(1);
    expect(String(postCalls[0][0])).toBe("/api/messages/9/reexplain");
    expect(JSON.parse(String(postCalls[0][1]?.body))).toEqual({ audience: "ba" });
    expect(mock.mock.calls.some((c) => String(c[0]).startsWith("/api/ask"))).toBe(false);
  });
});

/**
 * A stream the test drives frame by frame: each read waits until the test
 * pushes the next one. The reader who scrolls away mid-answer only exists in
 * the gap between two frames, and a fake that hands over every frame at once
 * has no such gap.
 */
function pushableStream(messages: unknown = null) {
  const encoder = new TextEncoder();
  const queue: string[] = [];
  let wake: (() => void) | null = null;
  let ended = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      // A stored thread, when the test opens one before asking. Routed like
      // routedFetch: the two endpoints must not answer the same way.
      if (messages !== null && String(url).startsWith("/api/threads/")) {
        return { ok: true, status: 200, json: async () => messages };
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
    }),
  );
  const bump = () => {
    const w = wake;
    wake = null;
    w?.();
  };
  return {
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

/** The scrolling element, given the heights jsdom never lays out. */
function scroller(container: HTMLElement, height = 2000) {
  const el = container.querySelector(".overflow-auto") as HTMLElement;
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: height });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: 500 });
  return el;
}

async function askInto(container: HTMLElement) {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Question"), "How?");
  await user.click(screen.getByRole("button", { name: "Ask" }));
  return container;
}

describe("Ask, following the answer", () => {
  it("scrolls the arriving answer into view", async () => {
    const stream = pushableStream();
    const { container } = render(<Ask />);
    await askInto(container);
    const view = scroller(container);

    await stream.push(ev("token", { text: "Shipping runs through a job." }));
    await screen.findByText(/Shipping runs/);

    expect(view.scrollTop).toBe(2000);
  });

  it("leaves the reader alone once they scrolled up, and follows again at the bottom", async () => {
    const stream = pushableStream();
    const { container } = render(<Ask />);
    await askInto(container);
    const view = scroller(container);

    await stream.push(ev("token", { text: "One. " }));
    // The reader scrolls back to read what has already arrived.
    view.scrollTop = 0;
    fireEvent.scroll(view);

    await stream.push(ev("token", { text: "Two. " }));
    await screen.findByText(/Two/);
    expect(view.scrollTop).toBe(0);

    // Back at the bottom, the answer is followed again.
    view.scrollTop = 1500;
    fireEvent.scroll(view);
    await stream.push(ev("token", { text: "Three." }));
    await screen.findByText(/Three/);
    expect(view.scrollTop).toBe(2000);
  });

  it("opens a stored thread at its top, never at the foot", async () => {
    routedFetch([storedTurn]);
    const { container, rerender } = render(<Ask threadId={null} onThread={() => {}} />);
    const view = scroller(container);
    view.scrollTop = 1500;

    rerender(<Ask threadId={7} onThread={() => {}} />);
    await screen.findByText(/Through a grant/);

    expect(view.scrollTop).toBe(0);
  });

  it("follows the answer again once a question is asked in the opened thread", async () => {
    const stream = pushableStream([storedTurn]);
    const { container, rerender } = render(<Ask threadId={null} onThread={() => {}} />);
    const view = scroller(container);
    rerender(<Ask threadId={7} onThread={() => {}} />);
    await screen.findByText(/Through a grant/);
    expect(view.scrollTop).toBe(0);

    await askInto(container);
    await stream.push(ev("token", { text: "Shipping runs through a job." }));
    await screen.findByText(/Shipping runs/);

    expect(view.scrollTop).toBe(2000);
  });

  it("fades the text that streams in", async () => {
    const stream = pushableStream();
    const { container } = render(<Ask />);
    await askInto(container);

    await stream.push(ev("token", { text: "Shipping runs through a job." }));
    await screen.findByText(/Shipping runs/);
    expect(container.querySelectorAll(".stream-seg").length).toBeGreaterThan(0);
    await stream.end();
  });

  it("does not fade a stored thread: its answers were read long ago", async () => {
    // Opening a thread mounts every answer at once. A fade there would wash
    // the whole conversation in, as if it were arriving now.
    routedFetch([storedTurn]);
    const { container } = render(
      <StrictMode>
        <Ask threadId={7} />
      </StrictMode>,
    );
    await screen.findByText(/Through a grant/);
    expect(container.querySelector(".stream-seg")).toBeNull();
  });
});

describe("Ask, the answer language across a reload", () => {
  // The composer's default only: a turn keeps the language it was asked in.
  it("opens on the language last picked", async () => {
    const user = userEvent.setup();
    const { unmount } = render(<Ask />);
    await user.selectOptions(screen.getByLabelText("Answer language"), "de");
    expect(localStorage.getItem("rongo.language")).toBe("de");

    unmount();
    render(<Ask />);
    expect((screen.getByLabelText("Answer language") as HTMLSelectElement).value).toBe("de");
  });

  // A code the backend's allowlist does not carry would be rejected on the
  // next question, and the reader would never learn why.
  it("falls back to English on a stored code that is not on the allowlist", () => {
    localStorage.setItem("rongo.language", "kl");
    render(<Ask />);
    expect((screen.getByLabelText("Answer language") as HTMLSelectElement).value).toBe("en");
  });

  // Safari's private mode throws on storage access. A forgotten preference is
  // a small annoyance; a blank page instead of the composer is not.
  it("still renders where storage throws", () => {
    const get = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    render(<Ask />);
    expect((screen.getByLabelText("Answer language") as HTMLSelectElement).value).toBe("en");
    get.mockRestore();
  });
});

describe("Ask, the caret of a streaming answer", () => {
  // The caret used to be a sibling element of the markdown, which made it a
  // block of its own: it blinked on the line BELOW the words it belongs to.
  // It is now drawn on the last block itself (index.css), so what the markup
  // has to get right is the marker class and which block comes last.
  it("marks the answer block as streaming so the caret sits on its last line", async () => {
    streamFrames([ev("thread", { thread_id: 1, message_id: 2 }), ev("token", { text: "Indexing walks the repo." })]);
    await ask("How does indexing work?");

    const para = await screen.findByText(
      (_, el) => el?.tagName === "P" && (el.textContent ?? "").includes("Indexing walks the repo"),
    );
    const block = document.querySelector(".ui-markdown.streaming");
    expect(block).toBeTruthy();
    expect(block?.lastElementChild).toBe(para);
    expect(block?.querySelector(".caret")).toBe(null);
  });

  it("drops the streaming mark once the answer is done", async () => {
    streamFrames([
      ev("thread", { thread_id: 1, message_id: 2 }),
      ev("token", { text: "Indexing walks the repo." }),
      ev("done", { message_id: 2 }),
    ]);
    await ask("How does indexing work?");

    await screen.findByText(
      (_, el) => el?.tagName === "P" && (el.textContent ?? "").includes("Indexing walks the repo"),
    );
    await waitFor(() => expect(document.querySelector(".ui-markdown.streaming")).toBe(null));
    expect(document.querySelector(".ui-markdown")).toBeTruthy();
  });
});

describe("Ask, the composer on a phone", () => {
  // A field rendering under 16px makes iOS Safari zoom the page in on focus
  // and never zoom back out: the whole app is then permanently wider than the
  // viewport. Both fields, not just the textarea — the language select is
  // text-xs by inheritance and is one tap away.
  it("gives both fields 16px on a touch screen", () => {
    render(<Ask />);
    const box = screen.getByLabelText("Question");
    expect(box.className).toContain("pointer-coarse:text-base");
    // An inline fontSize would out-specify the variant and put the zoom back.
    // (The textarea does carry an inline height — that is the autosize.)
    expect((box as HTMLTextAreaElement).style.fontSize).toBe("");
    const lang = screen.getByLabelText("Answer language");
    expect(lang.className).toContain("pointer-coarse:text-base");
    expect((lang as HTMLSelectElement).style.fontSize).toBe("");
  });

  it("wraps the controls instead of crushing them", () => {
    const { container } = render(<Ask />);
    const row = container.querySelector("form .flex.flex-wrap");
    expect(row).toBeTruthy();
    expect(screen.getByRole("group", { name: "Role" }).parentElement).toBe(row);
  });

  // ml-auto used to sit on the hint itself, which is hidden below sm — so on a
  // phone the hint was display:none and the Ask button lost its push to the
  // right edge and sat against the language pill.
  it("keeps Ask on the right edge where the hint is not rendered", () => {
    render(<Ask />);
    const hint = screen.getByText("Shift+Enter for a new line");
    expect(hint.className).not.toContain("ml-auto");
    expect(hint.parentElement?.className).toContain("ml-auto");
    expect(hint.parentElement?.contains(screen.getByRole("button", { name: "Ask" }))).toBe(true);
  });
});
