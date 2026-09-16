import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Ask from "./Ask";
import { asMarkdown, freshTurn } from "./turns";
import { PASTE_CHAR_THRESHOLD, PASTE_LINE_THRESHOLD, MAX_QUESTION_BYTES } from "./pastes";

/**
 * The paste path of the composer. Kept apart from Ask.test.tsx, which is
 * long already and knows nothing about pastes: every body it asserts with
 * toEqual stays exactly as it was, because a turn without a paste sends no
 * pasted_texts key at all.
 */

const ev = (name: string, data: unknown) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;

function queuedFetch(responses: string[][], threadsJson: unknown = []) {
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

const posts = (mock: ReturnType<typeof vi.fn>) =>
  mock.mock.calls.filter((c) => c[1]?.method === "POST").map((c) => [c[0], JSON.parse(String(c[1]?.body))] as const);

/** A paste event as a browser fires it; false back means the composer took it. */
function paste(text: string) {
  return fireEvent.paste(screen.getByLabelText("Question"), { clipboardData: { getData: () => text } });
}

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

const trace = Array.from({ length: PASTE_LINE_THRESHOLD + 5 }, (_, i) => `main.go:${i + 1}`).join("\n");

describe("Ask, pasting", () => {
  it("stages a wall of lines as a chip and leaves the textarea alone", () => {
    queuedFetch([]);
    strict(<Ask />);

    expect(paste(trace + "\n")).toBe(false);

    expect((screen.getByLabelText("Question") as HTMLTextAreaElement).value).toBe("");
    const chip = screen.getByRole("button", { name: `Pasted text · ${PASTE_LINE_THRESHOLD + 5} lines` });
    expect(chip.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/main\.go:1/)).toBeNull();
  });

  it("stages a long single line too", () => {
    queuedFetch([]);
    strict(<Ask />);

    expect(paste("x".repeat(PASTE_CHAR_THRESHOLD + 1))).toBe(false);

    expect(screen.getByRole("button", { name: "Pasted text · 1 line" })).toBeTruthy();
  });

  it("lets a short paste through to the browser", () => {
    queuedFetch([]);
    strict(<Ask />);

    expect(paste("just a sentence")).toBe(true);

    expect(screen.queryByRole("button", { name: /Pasted text/ })).toBeNull();
  });

  it("takes a chip back on its ×", async () => {
    queuedFetch([]);
    strict(<Ask />);
    paste(trace);

    await userEvent.setup().click(screen.getByRole("button", { name: "Remove pasted text" }));

    expect(screen.queryByRole("button", { name: /Pasted text/ })).toBeNull();
  });

  it("sends a paste-only question folded, and says which part was pasted", async () => {
    const mock = queuedFetch([[ev("thread", { thread_id: "1", message_id: 5 }), ev("done", { message_id: 5 })]]);
    strict(<Ask />);
    paste(trace + "\n");

    await userEvent.setup().click(screen.getByRole("button", { name: "Ask" }));

    await waitFor(() => expect(posts(mock).length).toBe(1));
    expect(posts(mock)[0][1]).toEqual({
      question: trace,
      audience: "ba",
      language: "en",
      thread_id: "",
      pasted_texts: [{ text: trace, lines: PASTE_LINE_THRESHOLD + 5 }],
    });
    // The composer is clear for the next question, chips included.
    expect(screen.queryByRole("button", { name: "Remove pasted text" })).toBeNull();
    // The turn shows the chip, folded, and no prose above it: nothing was typed.
    expect(screen.getByRole("button", { name: `Pasted text · ${PASTE_LINE_THRESHOLD + 5} lines` })).toBeTruthy();
    expect(screen.queryByText(/main\.go:1/)).toBeNull();
  });

  it("puts the typed words first and the paste after them", async () => {
    const mock = queuedFetch([[ev("thread", { thread_id: "1", message_id: 5 }), ev("done", { message_id: 5 })]]);
    strict(<Ask />);
    const user = userEvent.setup();
    paste(trace);
    await user.type(screen.getByLabelText("Question"), "Why does this fail?");

    await user.click(screen.getByRole("button", { name: "Ask" }));

    await waitFor(() => expect(posts(mock).length).toBe(1));
    expect(posts(mock)[0][1].question).toBe("Why does this fail?\n\n" + trace);
    expect(await screen.findByText("Why does this fail?")).toBeTruthy();
    expect(screen.getByRole("button", { name: `Pasted text · ${PASTE_LINE_THRESHOLD + 5} lines` })).toBeTruthy();
  });

  it("drops the selection a paste replaces, so select-all and paste does not keep the old draft", async () => {
    const mock = queuedFetch([[ev("thread", { thread_id: "1", message_id: 5 }), ev("done", { message_id: 5 })]]);
    strict(<Ask />);
    const user = userEvent.setup();
    const box = screen.getByLabelText("Question") as HTMLTextAreaElement;
    await user.type(box, "old draft");
    box.setSelectionRange(0, box.value.length);

    paste(trace);

    expect(box.value).toBe("");
    await user.click(screen.getByRole("button", { name: "Ask" }));
    await waitFor(() => expect(posts(mock).length).toBe(1));
    expect(posts(mock)[0][1].question).toBe(trace);
  });

  it("refuses a paste that would take the question over the cap and keeps the draft", async () => {
    queuedFetch([]);
    strict(<Ask />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Question"), "Why?");
    paste(trace);

    // Two bytes a character: half the cap in umlauts is the whole cap in
    // bytes, and the question and the first chip are already on the scale.
    expect(paste("ä".repeat(MAX_QUESTION_BYTES / 2))).toBe(false);

    expect(screen.getByRole("alert").textContent).toContain("32 KB");
    expect((screen.getByLabelText("Question") as HTMLTextAreaElement).value).toBe("Why?");
    expect(screen.getAllByRole("button", { name: /Pasted text/ }).length).toBe(1);
    expect(globalThis.fetch).not.toHaveBeenCalled();

    // Typing clears the notice.
    await user.type(screen.getByLabelText("Question"), " Really.");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("refuses to ask when typing after a paste has crossed the cap", async () => {
    queuedFetch([]);
    strict(<Ask />);
    const box = screen.getByLabelText("Question") as HTMLTextAreaElement;
    paste("a".repeat(MAX_QUESTION_BYTES - 10));
    fireEvent.change(box, { target: { value: "b".repeat(20) } });

    await userEvent.setup().click(screen.getByRole("button", { name: "Ask" }));

    expect(screen.getByRole("alert")).toBeTruthy();
    expect(globalThis.fetch).not.toHaveBeenCalled();
    expect(box.value).toBe("b".repeat(20));
  });

  it("carries the fold on a retry and on a re-explain", async () => {
    const mock = queuedFetch([
      [ev("thread", { thread_id: "1", message_id: 5 }), ev("error", { message: "The turn failed.", message_id: 5 })],
      [ev("thread", { thread_id: "1", message_id: 6 }), ev("token", { text: "The answer." }), ev("done", { message_id: 6 })],
    ]);
    strict(<Ask />);
    const user = userEvent.setup();
    paste(trace);
    await user.click(screen.getByRole("button", { name: "Ask" }));

    await user.click(await screen.findByRole("button", { name: "Retry" }));

    await screen.findByText("The answer.");
    expect(posts(mock).length).toBe(2);
    expect(posts(mock)[1][1]).toEqual({
      question: trace,
      audience: "ba",
      language: "en",
      thread_id: "1",
      head_message_id: 5,
      pasted_texts: [{ text: trace, lines: PASTE_LINE_THRESHOLD + 5 }],
    });
    // One question, one article, the chip drawn once for the whole turn.
    expect(screen.getAllByRole("article").length).toBe(1);
    expect(screen.getAllByRole("button", { name: /Pasted text/ }).length).toBe(1);
  });

  it("carries the fold when a candidate is chosen off a card", async () => {
    const candidate = { idx: 0, title: "Through the login service", summary: "Sign-in.", repo: "peeq", branch: "master" };
    const mock = queuedFetch([
      [
        ev("thread", { thread_id: "7", message_id: 5 }),
        ev("clarification", { message_id: 5, candidates: [candidate] }),
        ev("done", { message_id: 5 }),
      ],
      [ev("thread", { thread_id: "7", message_id: 6 }), ev("token", { text: "Done." }), ev("done", { message_id: 6 })],
    ]);
    strict(<Ask />);
    const user = userEvent.setup();
    paste(trace);
    await user.click(screen.getByRole("button", { name: "Ask" }));

    await user.click(await screen.findByText("Through the login service"));

    await waitFor(() => expect(posts(mock).length).toBe(2));
    expect(posts(mock)[1][1]).toMatchObject({
      question: trace,
      clarification_message_id: 5,
      choice: 0,
      pasted_texts: [{ text: trace, lines: PASTE_LINE_THRESHOLD + 5 }],
    });
  });
});

const stored = {
  id: 9,
  ordinal: 0,
  audience: "ba",
  language: "en",
  question: "Why does this fail?\n\n" + trace,
  pasted_texts: [{ text: trace, lines: PASTE_LINE_THRESHOLD + 5 }],
  answer: "Because.",
  error: "",
  citations: [],
  from_candidate_idx: -1,
  from_clarification_id: 0,
  created_at: "2026-09-16T10:00:00Z",
};

describe("Ask, a stored turn with a paste", () => {
  it("draws the typed words as prose and the paste as a chip", async () => {
    queuedFetch([], [stored]);
    strict(<Ask threadId="7" />);

    expect(await screen.findByText("Why does this fail?")).toBeTruthy();
    const chip = screen.getByRole("button", { name: `Pasted text · ${PASTE_LINE_THRESHOLD + 5} lines` });
    expect(chip.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("button", { name: "Remove pasted text" })).toBeNull();

    await userEvent.setup().click(chip);
    expect(screen.getByText(trace, { collapseWhitespace: false }).tagName).toBe("PRE");
  });

  it("leaves a block in the prose when it is not where the fold put it", async () => {
    queuedFetch([], [{ ...stored, pasted_texts: [{ text: "something else", lines: 1 }] }]);
    strict(<Ask threadId="7" />);

    expect(await screen.findByText(new RegExp("Why does this fail\\?"))).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Pasted text/ })).toBeNull();
    expect(screen.getByText(/main\.go:1/)).toBeTruthy();
  });

  it("builds a retry that carries the fold from a failed stored row", async () => {
    const mock = queuedFetch(
      [[ev("thread", { thread_id: "7", message_id: 10 }), ev("token", { text: "Now." }), ev("done", { message_id: 10 })]],
      [{ ...stored, answer: "", error: "The turn failed." }],
    );
    strict(<Ask threadId="7" />);

    await userEvent.setup().click(await screen.findByRole("button", { name: "Retry" }));

    await waitFor(() => expect(posts(mock).length).toBe(1));
    expect(posts(mock)[0][1]).toMatchObject({
      question: stored.question,
      head_message_id: 9,
      pasted_texts: stored.pasted_texts,
    });
  });
});

describe("asMarkdown with a paste", () => {
  it("heads with the typed words and fences the paste", () => {
    const turn = { ...freshTurn("Why?\n\npanic: boom", "ba", "en", null, [{ text: "panic: boom", lines: 1 }]), text: "Because." };
    expect(asMarkdown(turn)).toBe("# Why?\n\n```\npanic: boom\n```\n\nBecause.\n");
  });

  it("has no heading for a paste-only turn", () => {
    const turn = { ...freshTurn("panic: boom", "ba", "en", null, [{ text: "panic: boom", lines: 1 }]), text: "Because." };
    expect(asMarkdown(turn)).toBe("```\npanic: boom\n```\n\nBecause.\n");
  });

  it("is unchanged for a turn without one", () => {
    const turn = { ...freshTurn("Why?", "ba", "en"), text: "Because." };
    expect(asMarkdown(turn)).toBe("# Why?\n\nBecause.\n");
  });
});
