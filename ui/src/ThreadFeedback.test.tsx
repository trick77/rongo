import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ThreadFeedback from "./ThreadFeedback";
import { freshTurn, type Turn } from "./turns";

/**
 * The reader's verdict on a thread rides on the caveat under the composer:
 * one per thread, thumbs only once there is a finished answer to judge, and a
 * reason offered after a thumbs down without ever adding a row.
 */

const caveat = "Rongo can make mistakes. Please double-check responses.";

function answered(id: number, headId: number | null = null): Turn {
  return { ...freshTurn("How?", "ba", "en", headId), text: "Like so.", done: true, messageId: id, recorded: true };
}

type Call = { url: string; method: string; body?: unknown };

function server(stored: unknown) {
  const calls: Call[] = [];
  const mock = vi.fn(async (url: string, opts?: RequestInit) => {
    const method = opts?.method ?? "GET";
    const body = opts?.body ? JSON.parse(String(opts.body)) : undefined;
    calls.push({ url, method, body });
    if (method === "PUT") {
      const b = body as { verdict: number; reason?: string };
      return { ok: true, status: 200, json: async () => ({ verdict: b.verdict, reason: b.reason ?? "", upToMessageId: 2 }) };
    }
    if (method === "DELETE") return { ok: true, status: 204, json: async () => null };
    return { ok: true, status: 200, json: async () => stored };
  });
  vi.stubGlobal("fetch", mock);
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ThreadFeedback", () => {
  it("shows the caveat alone while there is nothing finished to judge", () => {
    server(null);
    render(<ThreadFeedback threadId="t1" turns={[]} running={false} caveat={caveat} />);
    expect(screen.getByText(caveat)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Helpful" })).toBeNull();
  });

  it("shows the caveat alone while a turn is running", () => {
    server(null);
    render(<ThreadFeedback threadId="t1" turns={[answered(1)]} running caveat={caveat} />);
    expect(screen.queryByRole("button", { name: "Helpful" })).toBeNull();
  });

  it("shows the caveat alone with no thread open", () => {
    server(null);
    render(<ThreadFeedback threadId={null} turns={[answered(1)]} running={false} caveat={caveat} />);
    expect(screen.queryByRole("button", { name: "Helpful" })).toBeNull();
  });

  it("stores a thumbs up and clears it on a second click", async () => {
    const calls = server(null);
    const user = userEvent.setup();
    render(<ThreadFeedback threadId="t1" turns={[answered(2)]} running={false} caveat={caveat} />);

    const up = await screen.findByRole("button", { name: "Helpful" });
    await user.click(up);
    await waitFor(() => expect(up.getAttribute("aria-pressed")).toBe("true"));
    expect(calls).toContainEqual({ url: "/api/threads/t1/feedback", method: "PUT", body: { verdict: 1, reason: "" } });

    await user.click(up);
    await waitFor(() => expect(up.getAttribute("aria-pressed")).toBe("false"));
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("offers the reasons after a thumbs down, stores the pick and lets it change", async () => {
    const calls = server(null);
    const user = userEvent.setup();
    render(<ThreadFeedback threadId="t1" turns={[answered(2)]} running={false} caveat={caveat} />);

    await user.click(await screen.findByRole("button", { name: "Not helpful" }));
    // The reasons take the line: the caveat steps aside while they are offered.
    expect(await screen.findByText("What was off?")).toBeTruthy();
    expect(screen.queryByText(caveat)).toBeNull();

    await user.click(screen.getByRole("button", { name: "Incomplete" }));
    // The pressed thumb says "not helpful"; the words add only the reason.
    expect(await screen.findByText("Incomplete")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Not helpful" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByText(caveat)).toBeTruthy();
    expect(calls.at(-1)).toEqual({ url: "/api/threads/t1/feedback", method: "PUT", body: { verdict: -1, reason: "incomplete" } });

    await user.click(screen.getByRole("button", { name: "change" }));
    expect(await screen.findByText("What was off?")).toBeTruthy();
  });

  it("lets the reasons be skipped, keeping the bare thumbs down", async () => {
    const calls = server(null);
    const user = userEvent.setup();
    render(<ThreadFeedback threadId="t1" turns={[answered(2)]} running={false} caveat={caveat} />);

    await user.click(await screen.findByRole("button", { name: "Not helpful" }));
    await user.click(await screen.findByRole("button", { name: "skip" }));

    expect(screen.queryByText("What was off?")).toBeNull();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(1);
  });

  it("reads back a stored verdict and says turns came after it", async () => {
    server({ verdict: 1, reason: "", upToMessageId: 2 });
    const turns = [answered(1), answered(2), answered(3)];
    render(<ThreadFeedback threadId="t1" turns={turns} running={false} caveat={caveat} />);

    expect(await screen.findByText("Helpful · rated before turn 3")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Helpful" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("counts a re-explained answer as the turn it belongs to, not a newer one", async () => {
    server({ verdict: 1, reason: "", upToMessageId: 2 });
    const turns = [answered(1), answered(2, 1)];
    render(<ThreadFeedback threadId="t1" turns={turns} running={false} caveat={caveat} />);

    expect(await screen.findByText("Helpful")).toBeTruthy();
    expect(screen.queryByText(/rated before/)).toBeNull();
  });

  it("counts a turn as covered when the verdict came after its answer, whatever was re-explained since", async () => {
    // Q1 (1), Q2 (2), then Q1 re-explained (3), then rated: the newest answer
    // is the re-explain, and Q2 was on screen when the reader judged.
    server({ verdict: 1, reason: "", upToMessageId: 3 });
    const turns = [answered(1), answered(2), answered(3, 1)];
    render(<ThreadFeedback threadId="t1" turns={turns} running={false} caveat={caveat} />);

    expect(await screen.findByText("Helpful")).toBeTruthy();
    expect(screen.queryByText(/rated before/)).toBeNull();
  });

  it("drops an open reason picker when the next turn starts", async () => {
    server(null);
    const user = userEvent.setup();
    const first = [answered(2)];
    const { rerender } = render(<ThreadFeedback threadId="t1" turns={first} running={false} caveat={caveat} />);
    await user.click(await screen.findByRole("button", { name: "Not helpful" }));
    expect(await screen.findByText("What was off?")).toBeTruthy();

    // A reason picked after the next answer would pin the verdict to an
    // answer the reader never judged.
    rerender(<ThreadFeedback threadId="t1" turns={first} running caveat={caveat} />);
    rerender(<ThreadFeedback threadId="t1" turns={[...first, answered(4)]} running={false} caveat={caveat} />);

    expect(screen.queryByText("What was off?")).toBeNull();
    expect(screen.getByRole("button", { name: "Not helpful" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("keeps a click made before the stored verdict arrived", async () => {
    // The load answers "none" only after the reader already voted: the read
    // ran first on the server, its reply is older than the click.
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, opts?: RequestInit) => {
        if ((opts?.method ?? "GET") === "GET") {
          await gate;
          return { ok: true, status: 200, json: async () => null };
        }
        return { ok: true, status: 200, json: async () => ({ verdict: 1, reason: "", upToMessageId: 2 }) };
      }),
    );
    const user = userEvent.setup();
    render(<ThreadFeedback threadId="t1" turns={[answered(2)]} running={false} caveat={caveat} />);

    const up = screen.getByRole("button", { name: "Helpful" });
    await user.click(up);
    await waitFor(() => expect(up.getAttribute("aria-pressed")).toBe("true"));
    release();
    await new Promise((r) => setTimeout(r, 0));

    expect(up.getAttribute("aria-pressed")).toBe("true");
  });

  it("treats a reply that is not a verdict as none", async () => {
    server([]);
    render(<ThreadFeedback threadId="t1" turns={[answered(2)]} running={false} caveat={caveat} />);
    expect(await screen.findByText("Was this thread helpful?")).toBeTruthy();
  });
});
