import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ThreadFeedback from "./ThreadFeedback";
import { freshTurn, type Turn } from "./turns";

/**
 * The reader's verdict on a thread, among the buttons under the newest
 * answer: one per thread, and a reason offered after a thumbs down in a row
 * of its own.
 */

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

function row(turns: Turn[], threadId = "t1") {
  return (
    <div className="flex flex-wrap">
      <ThreadFeedback threadId={threadId} turns={turns} />
    </div>
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ThreadFeedback", () => {
  it("stores a thumbs up and clears it on a second click", async () => {
    const calls = server(null);
    const user = userEvent.setup();
    render(row([answered(2)]));

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
    render(row([answered(2)]));

    await user.click(await screen.findByRole("button", { name: "Not helpful" }));
    expect(await screen.findByText("What was off?")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Incomplete" }));
    await waitFor(() => expect(screen.queryByText("What was off?")).toBeNull());
    expect(calls.at(-1)).toEqual({ url: "/api/threads/t1/feedback", method: "PUT", body: { verdict: -1, reason: "incomplete" } });

    await user.click(screen.getByRole("button", { name: "change reason" }));
    expect(await screen.findByText("What was off?")).toBeTruthy();
  });

  it("lets the reasons be skipped, keeping the bare thumbs down", async () => {
    const calls = server(null);
    const user = userEvent.setup();
    render(row([answered(2)]));

    await user.click(await screen.findByRole("button", { name: "Not helpful" }));
    await user.click(await screen.findByRole("button", { name: "skip" }));

    expect(screen.queryByText("What was off?")).toBeNull();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "add a reason" })).toBeTruthy();
  });

  it("reads back a stored verdict and names the first turn it does not cover", async () => {
    server({ verdict: -1, reason: "wrong", upToMessageId: 2 });
    render(row([answered(1), answered(2), answered(3)]));

    expect(await screen.findByText("Wrong")).toBeTruthy();
    expect(screen.getByText("rated before turn 3")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Not helpful" }).getAttribute("aria-pressed")).toBe("true");
    // A reason saved now would cover turn 3 too, which the reader never judged.
    expect(screen.queryByRole("button", { name: "change reason" })).toBeNull();
  });

  it("counts a turn as covered when the verdict came after its answer, whatever was re-explained since", async () => {
    // Q1 (1), Q2 (2), then Q1 re-explained (3), then rated: the newest answer
    // is the re-explain, and Q2 was on screen when the reader judged.
    server({ verdict: 1, reason: "", upToMessageId: 3 });
    render(row([answered(1), answered(2), answered(3, 1)]));

    expect(await screen.findByText("Helpful")).toBeTruthy();
    expect(screen.queryByText(/rated before/)).toBeNull();
  });

  it("closes the picker once a newer answer is on screen", async () => {
    server(null);
    const user = userEvent.setup();
    const first = [answered(2)];
    const { rerender } = render(row(first));
    await user.click(await screen.findByRole("button", { name: "Not helpful" }));
    expect(await screen.findByText("What was off?")).toBeTruthy();

    rerender(row([...first, answered(4)]));

    expect(screen.queryByText("What was off?")).toBeNull();
  });

  it("never opens the picker for a thumbs down whose save returns after the next answer", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, opts?: RequestInit) => {
        if (opts?.method === "PUT") {
          await gate;
          return { ok: true, status: 200, json: async () => ({ verdict: -1, reason: "", upToMessageId: 2 }) };
        }
        return { ok: true, status: 200, json: async () => null };
      }),
    );
    const user = userEvent.setup();
    const first = [answered(2)];
    const { rerender } = render(row(first));
    await user.click(await screen.findByRole("button", { name: "Not helpful" }));

    rerender(row([...first, answered(4)]));
    release();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Not helpful" }).getAttribute("aria-pressed")).toBe("true"),
    );

    expect(screen.queryByText("What was off?")).toBeNull();
  });

  it("takes no second verdict while the first is still being saved", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const puts: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, opts?: RequestInit) => {
        if (opts?.method === "PUT") {
          puts.push(JSON.parse(String(opts.body)));
          await gate;
          return { ok: true, status: 200, json: async () => ({ verdict: -1, reason: "", upToMessageId: 2 }) };
        }
        return { ok: true, status: 200, json: async () => null };
      }),
    );
    const user = userEvent.setup();
    render(row([answered(2)]));

    await user.click(await screen.findByRole("button", { name: "Not helpful" }));
    await user.click(screen.getByRole("button", { name: "Helpful" }));
    release();

    await screen.findByText("What was off?");
    expect(puts).toEqual([{ verdict: -1, reason: "" }]);
  });

  it("keeps a click made before the stored verdict arrived", async () => {
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
    render(row([answered(2)]));

    const up = screen.getByRole("button", { name: "Helpful" });
    await user.click(up);
    await waitFor(() => expect(up.getAttribute("aria-pressed")).toBe("true"));
    release();
    await new Promise((r) => setTimeout(r, 0));

    expect(up.getAttribute("aria-pressed")).toBe("true");
  });

  it("treats a reply that is not a verdict as none", async () => {
    server([]);
    render(row([answered(2)]));
    expect(await screen.findByText("Helpful?")).toBeTruthy();
  });
});
