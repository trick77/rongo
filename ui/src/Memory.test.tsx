import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Ask from "./Ask";
import Trace from "./Trace";
import { storedTurn } from "./turns";

/**
 * A standing instruction given in chat: the stream announces what was kept,
 * the turn carries it as a chip, and the record brings it back on a reload
 * for as long as the rule exists.
 */

const ev = (name: string, data: unknown) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;

function streamFrames(frames: string[]) {
  const encoder = new TextEncoder();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (String(url).startsWith("/api/threads/")) return { ok: true, status: 200, json: async () => [] };
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
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("memory in the thread", () => {
  it("draws the rule the turn kept, under the answer, with an undo", async () => {
    streamFrames([
      ev("thread", { thread_id: "1", title: "x", message_id: 5 }),
      ev("status", { step: "understanding" }),
      ev("status", { step: "remembering" }),
      ev("memory", { id: 7, text: "Never draw flowchart diagrams.", replaced: ["Always draw one."] }),
      ev("token", { text: 'Noted: "Never draw flowchart diagrams."' }),
      ev("citations", []),
      ev("done", { message_id: 5 }),
    ]);
    const user = userEvent.setup();
    render(
      <StrictMode>
        <Ask />
      </StrictMode>,
    );
    await user.type(screen.getByLabelText("Question"), "Zeig mir nie wieder Flowcharts.");
    await user.click(screen.getByRole("button", { name: "Ask" }));

    expect(await screen.findByText("Remembered")).toBeTruthy();
    expect(screen.getByText(/replaces "Always draw one\."/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Undo" })).toBeTruthy();
    // The step is on the timeline by its label.
    expect(screen.getByText("Remembering the instruction")).toBeTruthy();
  });

  it("a turn that only forgot draws the chip with nothing to undo", async () => {
    streamFrames([
      ev("thread", { thread_id: "1", title: "x", message_id: 5 }),
      ev("memory", { removed: ["Keep it short."], scope: "shop", scope_dropped: "" }),
      ev("token", { text: 'Forgotten: "Keep it short."' }),
      ev("citations", []),
      ev("done", { message_id: 5 }),
    ]);
    const user = userEvent.setup();
    render(
      <StrictMode>
        <Ask />
      </StrictMode>,
    );
    await user.type(screen.getByLabelText("Question"), "Forget the rule about length.");
    await user.click(screen.getByRole("button", { name: "Ask" }));

    const chip = await screen.findByRole("note", { name: "Memory" });
    expect(chip.textContent).toContain("Forgotten");
    expect(chip.textContent).toContain('"Keep it short."');
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("a stored turn carries the rule it saved and nothing more", () => {
    const t = storedTurn({
      id: 5,
      ordinal: 0,
      audience: "ba",
      question: "Never show me flowcharts.",
      answer: "Noted.",
      error: "",
      citations: [],
      clarification: null,
      from_candidate_idx: -1,
      from_clarification_id: 0,
      memory: { id: 7, text: "Never draw flowchart diagrams." },
    });
    expect(t.memory).toEqual({
      id: 7,
      text: "Never draw flowchart diagrams.",
      scope: "",
      replaced: [],
      removed: [],
      scopeDropped: "",
    });
    const none = storedTurn({
      id: 6,
      ordinal: 1,
      audience: "ba",
      question: "How?",
      answer: "So.",
      error: "",
      citations: [],
      clarification: null,
      from_candidate_idx: -1,
      from_clarification_id: 0,
    });
    expect(none.memory).toBeNull();
  });
});

describe("Trace, the remembering step", () => {
  const t0 = 1_000_000;

  it("says what was kept, what it replaced and what was forgotten", () => {
    const { container } = render(
      <StrictMode>
        <Trace
          steps={[
            {
              step: "remembering",
              at: t0,
              detail: { memory: "Never draw flowchart diagrams.", scope: "shop", replaced: ["Always draw one."], removed: ["Keep it short."] },
            },
          ]}
          state="done"
          startedAt={t0}
          endedAt={t0 + 100}
        />
      </StrictMode>,
    );
    const text = container.querySelector(".trace-detail")?.textContent ?? "";
    expect(text).toContain("Kept");
    expect(text).toContain("Never draw flowchart diagrams.");
    expect(text).toContain("for shop");
    expect(text).toContain("replaces");
    expect(text).toContain("forgot");
  });

  it("says a scope the index does not carry made the rule hold everywhere", () => {
    const { container } = render(
      <StrictMode>
        <Trace
          steps={[{ step: "remembering", at: t0, detail: { memory: "Skip the tests.", scope_dropped: "warehouse" } }]}
          state="done"
          startedAt={t0}
          endedAt={t0 + 100}
        />
      </StrictMode>,
    );
    expect(container.querySelector(".trace-detail")?.textContent).toContain("everywhere: warehouse is not indexed");
  });

  it("says one standing instruction in the singular", () => {
    const { container } = render(
      <StrictMode>
        <Trace
          steps={[{ step: "writing", at: t0, detail: { prompt_tokens: 100, completion_tokens: 10, cited: 1, sources: 2, memories: 1 } }]}
          state="done"
          startedAt={t0}
          endedAt={t0 + 1}
        />
      </StrictMode>,
    );
    expect(container.querySelector(".trace-detail")?.textContent).toContain("1 standing instruction");
  });

  it("says a full memory refused the rule", () => {
    render(
      <StrictMode>
        <Trace steps={[{ step: "remembering", at: t0, detail: { refused: "full" } }]} state="done" startedAt={t0} endedAt={t0 + 1} />
      </StrictMode>,
    );
    expect(screen.getByText(/the memory is full/)).toBeTruthy();
  });

  it("counts the standing instructions the answer was written under", () => {
    const { container } = render(
      <StrictMode>
        <Trace
          steps={[{ step: "writing", at: t0, detail: { prompt_tokens: 100, completion_tokens: 10, cited: 1, sources: 2, memories: 2 } }]}
          state="done"
          startedAt={t0}
          endedAt={t0 + 1}
        />
      </StrictMode>,
    );
    expect(container.querySelector(".trace-detail")?.textContent).toContain("2 standing instructions");
  });
});
