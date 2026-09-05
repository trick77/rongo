import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Ask from "./Ask";

/**
 * A finished answer offers what to ask next. The pills belong to the answer
 * they were written from: only the newest one shows them, a click asks the
 * question in that turn's own role and language, and the answer it came from
 * is left exactly as it was.
 */

const ev = (name: string, data: unknown) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;

/** Streams a fresh set of frames per call, so a second turn gets its own. */
function streamPerCall(frameSets: string[][]) {
  const encoder = new TextEncoder();
  let call = 0;
  const mock = vi.fn(async (url: string, _opts?: RequestInit) => {
    if (String(url).startsWith("/api/threads/")) {
      return { ok: true, status: 200, json: async () => [] };
    }
    const frames = frameSets[Math.min(call++, frameSets.length - 1)];
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

const answered = (followups: string[], text = "Through a grant.") => [
  ev("thread", { thread_id: 1, title: "x", message_id: 5 }),
  ev("token", { text }),
  ev("citations", []),
  ev("followups", followups),
  ev("done", { message_id: 5 }),
];

async function ask(text: string, language?: string) {
  const user = userEvent.setup();
  render(
    <StrictMode>
      <Ask />
    </StrictMode>,
  );
  if (language) await user.selectOptions(screen.getByLabelText("Answer language"), language);
  await user.type(screen.getByLabelText("Question"), text);
  await user.click(screen.getByRole("button", { name: "Ask" }));
  return user;
}

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("follow-up suggestions", () => {
  it("offers what to ask next once the answer is finished", async () => {
    streamPerCall([answered(["What happens on a re-index?", "Where is the SHA recorded?"])]);

    await ask("How are citations stored?");

    const nav = await screen.findByRole("navigation", { name: "Follow-up questions" });
    expect(nav.textContent).toContain("What happens on a re-index?");
    expect(screen.getByRole("button", { name: "Where is the SHA recorded?" })).toBeTruthy();
  });

  it("asks a suggested question in the answering turn's role and language", async () => {
    // The pill was written for a Developer, in German. Answering it as an
    // Analyst in English would be a different product from the one the
    // reader clicked in, so the turn's own settings win over the composer's
    // current ones.
    const mock = streamPerCall([answered(["Was passiert beim Neuindexieren?"]), answered([])]);
    const user = userEvent.setup();
    render(
      <StrictMode>
        <Ask />
      </StrictMode>,
    );
    await user.click(screen.getByRole("button", { name: "Developer" }));
    await user.selectOptions(screen.getByLabelText("Answer language"), "de");
    await user.type(screen.getByLabelText("Question"), "Wie werden Zitate gespeichert?");
    await user.click(screen.getByRole("button", { name: "Ask" }));
    await screen.findByRole("navigation", { name: "Follow-up questions" });

    // The reader moves the composer back before clicking the pill.
    await user.click(screen.getByRole("button", { name: "Analyst" }));
    await user.selectOptions(screen.getByLabelText("Answer language"), "en");
    await user.click(screen.getByRole("button", { name: "Was passiert beim Neuindexieren?" }));

    await waitFor(() => expect(mock.mock.calls.filter((c) => c[0] === "/api/ask").length).toBe(2));
    const second = JSON.parse(mock.mock.calls.filter((c) => c[0] === "/api/ask")[1][1].body);
    expect(second.question).toBe("Was passiert beim Neuindexieren?");
    expect(second.audience).toBe("dev");
    expect(second.language).toBe("de");
    expect(second.thread_id).toBe(1);
  });

  it("adds a turn and leaves the answer it came from alone", async () => {
    streamPerCall([answered(["What happens on a re-index?"]), answered([], "It is rebuilt.")]);

    const user = await ask("How are citations stored?");
    await screen.findByRole("navigation", { name: "Follow-up questions" });
    await user.click(screen.getByRole("button", { name: "What happens on a re-index?" }));

    expect(await screen.findByText(/It is rebuilt/)).toBeTruthy();
    expect(screen.getByText(/Through a grant/)).toBeTruthy();
    expect(screen.getByText("How are citations stored?")).toBeTruthy();
  });

  it("moves the pills to the newest answer instead of stacking them up", async () => {
    streamPerCall([
      answered(["What happens on a re-index?"]),
      answered(["Where is the SHA recorded?"], "It is rebuilt."),
    ]);

    const user = await ask("How are citations stored?");
    await screen.findByRole("navigation", { name: "Follow-up questions" });
    await user.click(screen.getByRole("button", { name: "What happens on a re-index?" }));

    expect(await screen.findByRole("button", { name: "Where is the SHA recorded?" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "What happens on a re-index?" })).toBeNull();
    expect(screen.getAllByRole("navigation", { name: "Follow-up questions" }).length).toBe(1);
  });

  it("shows nothing when the turn was offered nothing", async () => {
    streamPerCall([answered([])]);

    await ask("How are citations stored?");

    await screen.findByText(/Through a grant/);
    expect(screen.queryByRole("navigation", { name: "Follow-up questions" })).toBeNull();
  });

  it("restores the pills of a stored thread's last answer", async () => {
    const mock = vi.fn(async (url: string) => {
      if (String(url).startsWith("/api/threads/")) {
        return {
          ok: true,
          status: 200,
          json: async () => [
            {
              id: 9,
              ordinal: 0,
              audience: "dev",
              question: "How are citations stored?",
              answer: "Through a grant.",
              error: "",
              citations: [],
              followups: ["What happens on a re-index?"],
              created_at: "2026-08-17T10:00:00Z",
            },
          ],
        };
      }
      return { ok: true, status: 200, body: { getReader: () => ({ async read() { return { done: true, value: undefined }; } }) } };
    });
    vi.stubGlobal("fetch", mock);

    render(
      <StrictMode>
        <Ask threadId={7} />
      </StrictMode>,
    );

    expect(await screen.findByRole("button", { name: "What happens on a re-index?" })).toBeTruthy();
  });
});
