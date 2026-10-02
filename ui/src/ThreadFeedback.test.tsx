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

  it("offers the reasons after a thumbs down and stores the pick; a new reason is up and down again", async () => {
    const calls = server(null);
    const user = userEvent.setup();
    render(row([answered(2)]));
    const up = await screen.findByRole("button", { name: "Helpful" });
    const down = screen.getByRole("button", { name: "Not helpful" });

    await user.click(down);
    expect(await screen.findByText("What was off?")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Incomplete" }));
    await waitFor(() => expect(screen.queryByText("What was off?")).toBeNull());
    expect(calls.at(-1)).toEqual({ url: "/api/threads/t1/feedback", method: "PUT", body: { verdict: -1, reason: "incomplete" } });

    await user.click(up);
    await user.click(down);
    expect(await screen.findByText("What was off?")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /reason/ })).toBeNull();
  });

  it("stores a thumbs down without a reason as soon as it is clicked, with nothing to dismiss", async () => {
    const calls = server(null);
    const user = userEvent.setup();
    render(row([answered(2)]));

    await user.click(await screen.findByRole("button", { name: "Not helpful" }));

    await waitFor(() => expect(calls.filter((c) => c.method === "PUT")).toHaveLength(1));
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ verdict: -1, reason: "" });
    expect(screen.getByText("What was off?")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "skip" })).toBeNull();
  });

  it("reads back a stored verdict and names the first turn it does not cover", async () => {
    server({ verdict: -1, reason: "wrong", upToMessageId: 2 });
    render(row([answered(1), answered(2), answered(3)]));

    expect(await screen.findByText("rated before turn 3")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Not helpful" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("counts a turn as covered when the verdict came after its answer, whatever was re-explained since", async () => {
    // Q1 (1), Q2 (2), then Q1 re-explained (3), then rated: the newest answer
    // is the re-explain, and Q2 was on screen when the reader judged.
    server({ verdict: 1, reason: "", upToMessageId: 3 });
    render(row([answered(1), answered(2), answered(3, 1)]));

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Helpful" }).getAttribute("aria-pressed")).toBe("true"),
    );
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

  it("shows every click at once and saves the reader's last choice, one write at a time", async () => {
    // A slow server: clicks made while a save is out must neither be lost nor
    // race it. They show at once; the write after it carries the last one.
    const releases: (() => void)[] = [];
    const writes: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, opts?: RequestInit) => {
        const method = opts?.method ?? "GET";
        if (method === "GET") return { ok: true, status: 200, json: async () => null };
        const body = opts?.body ? JSON.parse(String(opts.body)) : null;
        writes.push(method === "DELETE" ? "DELETE" : body);
        await new Promise<void>((r) => releases.push(r));
        return { ok: true, status: 200, json: async () => ({ ...body, upToMessageId: 2 }) };
      }),
    );
    const user = userEvent.setup();
    render(row([answered(2)]));
    const up = await screen.findByRole("button", { name: "Helpful" });
    const down = screen.getByRole("button", { name: "Not helpful" });

    await user.click(down);
    await user.click(up);
    await user.click(down);
    await user.click(up);

    // On screen at once, though only the first write has gone out.
    expect(up.getAttribute("aria-pressed")).toBe("true");
    expect(writes).toEqual([{ verdict: -1, reason: "" }]);

    releases[0]();
    await waitFor(() => expect(writes).toHaveLength(2));
    releases[1]();

    expect(writes[1]).toEqual({ verdict: 1, reason: "" });
    await waitFor(() => expect(up.getAttribute("aria-pressed")).toBe("true"));
  });

  it("reads Helpful? before the thumbs, or the reason of a thumbs down", async () => {
    server(null);
    const user = userEvent.setup();
    render(row([answered(2)]));
    const up = await screen.findByRole("button", { name: "Helpful" });
    const pill = up.parentElement as HTMLElement;
    const label = () => pill.firstElementChild?.textContent;

    for (const name of ["Helpful", "Not helpful", "Helpful"]) {
      await user.click(screen.getByRole("button", { name }));
      expect(label()).toBe("Helpful?");
    }
    await user.click(screen.getByRole("button", { name: "Not helpful" }));
    await user.click(screen.getByRole("button", { name: "Too long" }));
    expect(label()).toBe("Too long");
    expect(screen.queryByText("Thanks")).toBeNull();
  });

  it("reads the stored verdict back when the server refuses a write", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, opts?: RequestInit) => {
        if ((opts?.method ?? "GET") === "GET") return { ok: true, status: 200, json: async () => null };
        return { ok: false, status: 500, json: async () => null };
      }),
    );
    const user = userEvent.setup();
    render(row([answered(2)]));
    const up = await screen.findByRole("button", { name: "Helpful" });

    await user.click(up);

    await waitFor(() => expect(up.getAttribute("aria-pressed")).toBe("false"));
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
