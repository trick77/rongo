import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("App", () => {
  it("renders the heading with text rongo", () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => [] })));
    render(<App />);
    const heading = screen.getByRole("heading", { level: 1 });
    expect(heading.textContent).toBe("rongo");
  });

  it("behaelt den laufenden Thread beim Wechsel auf Repos", async () => {
    // Unmounting Ask would drop the answer on screen while the stream keeps
    // writing into a dead component. The stored record only catches up once the
    // turn is finished, so a stream interrupted this way is lost for good.
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => [] })));
    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByLabelText("Frage"), "Eine Frage, die stehen bleiben muss");

    await user.click(screen.getByRole("button", { name: "Repos" }));
    await user.click(screen.getByRole("button", { name: "Fragen" }));

    expect((screen.getByLabelText("Frage") as HTMLTextAreaElement).value).toBe(
      "Eine Frage, die stehen bleiben muss",
    );
  });
});

/** Answers the thread list and one thread's turns separately. */
function apiFetch(threads: unknown, messages: unknown) {
  const mock = vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () => (String(url).startsWith("/api/threads/") ? messages : threads),
  }));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const oneThread = [{ id: 7, title: "Wie laeuft der Versand?", created_at: "2026-08-17T10:00:00Z" }];
const oneTurn = [
  {
    id: 1,
    ordinal: 0,
    audience: "ba",
    question: "Wie laeuft der Versand?",
    answer: "Ueber einen Job [1].",
    error: "",
    citations: [],
    created_at: "2026-08-17T10:00:00Z",
  },
];

describe("App, der Thread ueber einen Neuladen hinweg", () => {
  it("holt den zuletzt offenen Thread zurueck", async () => {
    localStorage.setItem("rongo.thread", "7");
    apiFetch(oneThread, oneTurn);
    render(<StrictMode><App /></StrictMode>);
    expect(await screen.findByText(/Ueber einen Job/)).toBeTruthy();
  });

  it("merkt sich den gewaehlten Thread", async () => {
    apiFetch(oneThread, oneTurn);
    const user = userEvent.setup();
    render(<StrictMode><App /></StrictMode>);
    await user.click(await screen.findByRole("button", { name: "Wie laeuft der Versand?" }));
    await waitFor(() => expect(localStorage.getItem("rongo.thread")).toBe("7"));
  });

  // A thread that is not yours, or was purged, comes back as an empty list with
  // status 200. Keeping the id would make every later reload open nothing.
  it("vergisst eine Thread-Nummer, die ins Leere fuehrt", async () => {
    localStorage.setItem("rongo.thread", "999");
    apiFetch([], []);
    render(<StrictMode><App /></StrictMode>);
    await waitFor(() => expect(localStorage.getItem("rongo.thread")).toBeNull());
  });
});
