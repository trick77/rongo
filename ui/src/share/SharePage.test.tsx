import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import SharePage from "./SharePage";

afterEach(() => vi.unstubAllGlobals());

const turn = {
  id: 1,
  ordinal: 0,
  audience: "ba",
  language: "en",
  question: "How does routing decide?",
  answer: "It is a ladder [1].",
  error: "",
  citations: [
    { marker: 1, repo: "rongo", branch: "master", path: "internal/ask/route.go", start_line: 1, end_line: 4, sha: "abc" },
  ],
  from_candidate_idx: -1,
  from_clarification_id: 0,
  created_at: "2026-09-04T14:02:00Z",
};

/** Answers the share read, and anything else with a 404. */
function shared(body: unknown, status = 200) {
  const mock = vi.fn(async (url: string) => ({
    ok: status === 200,
    status,
    text: async () => "",
    json: async () => (String(url).includes("/source") ? { content: "package ask\n" } : body),
  }));
  vi.stubGlobal("fetch", mock);
  return mock;
}

describe("SharePage", () => {
  it("shows the thread and its sources", async () => {
    shared({ title: "How routing decides", messages: [turn] });
    render(<SharePage token="tok" />);

    expect(await screen.findByRole("heading", { name: "How routing decides" })).toBeTruthy();
    await screen.findByText(/It is a ladder/);
    expect(screen.getByText("Shared · read-only")).toBeTruthy();
  });

  it("offers nothing a reader could press", async () => {
    shared({ title: "How routing decides", messages: [{ ...turn, followups: ["And then?"] }] });
    render(<SharePage token="tok" />);
    await screen.findByText(/It is a ladder/);

    // No composer, no rail, and none of the per-turn actions: a reader with no
    // session has no move to make.
    expect(screen.queryByLabelText("Question")).toBeNull();
    expect(screen.queryByRole("button", { name: "New question" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull();
    expect(screen.queryByText("Copy as Markdown")).toBeNull();
    expect(screen.queryByText("Explain as Developer")).toBeNull();
    expect(screen.queryByText("Retry")).toBeNull();
    expect(screen.queryByText("And then?")).toBeNull();
  });

  it("shows no token count or cost even if one arrives", async () => {
    // The server strips these; the page must not put them back if a stale
    // deployment sends them anyway.
    shared({
      title: "How routing decides",
      messages: [
        {
          ...turn,
          usage: { calls: [], prompt_tokens: 900, completion_tokens: 120, total_tokens: 1020, cost_usd: 0.004 },
        },
      ],
    });
    render(<SharePage token="tok" />);
    await screen.findByText(/It is a ladder/);

    expect(screen.queryByText(/1,020 tok/)).toBeNull();
    expect(screen.queryByText(/\$0\.004/)).toBeNull();
  });

  it("opens a citation through the share's own endpoint, never /api/source", async () => {
    const mock = shared({ title: "How routing decides", messages: [turn] });
    render(<SharePage token="tok" />);
    await screen.findByText(/It is a ladder/);

    fireEvent.click(await screen.findByText("route.go"));

    await waitFor(() =>
      expect(mock.mock.calls.some(([url]) => String(url).startsWith("/api/shares/tok/source?"))).toBe(true),
    );
    // /api/source takes any repo/path/sha and is a reader for the whole
    // indexed corpus. The public page must never reach it.
    expect(mock.mock.calls.some(([url]) => String(url).startsWith("/api/source"))).toBe(false);
  });

  it("says the link is gone without saying which kind of gone", async () => {
    shared(null, 404);
    render(<SharePage token="tok" />);

    // Revoked, mistyped and deleted are one answer, so the page has one line.
    expect(await screen.findByText("This link is no longer available.")).toBeTruthy();
  });

  it("marks itself noindex while it is on screen", async () => {
    shared({ title: "How routing decides", messages: [turn] });
    const { unmount } = render(<SharePage token="tok" />);
    await screen.findByText(/It is a ladder/);

    const meta = document.head.querySelector('meta[name="robots"]');
    expect(meta?.getAttribute("content")).toBe("noindex, nofollow");
    unmount();
    expect(document.head.querySelector('meta[name="robots"]')).toBeNull();
  });

  it("draws an unanswered card as a record, not as a question", async () => {
    shared({
      title: "Where does the retry budget live?",
      messages: [
        {
          ...turn,
          answer: "",
          citations: [],
          clarification: {
            id: 3,
            candidates: [
              { idx: 0, title: "Gather hop budget", summary: "How far a walk may travel.", repo: "rongo", branch: "master" },
              { idx: 1, title: "Indexer fetch retries", summary: "What a failed fetch may try again.", repo: "rongo", branch: "master" },
            ],
          },
        },
      ],
    });
    render(<SharePage token="tok" />);

    // Ochre means "your move", and a reader following a link has none.
    await screen.findByText("Asked back: which one was meant");
    expect(screen.queryByText("Which one do you mean?")).toBeNull();
    const candidate = screen.getByText("Gather hop budget").closest("button") as HTMLButtonElement;
    expect(candidate.disabled).toBe(true);
  });
});
