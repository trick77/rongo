import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

/**
 * Replaces window.location so a navigation can be observed instead of
 * performed, and so the gate can be given a query string to read.
 */
function stubLocation(href: (url: string) => void, search: string) {
  vi.stubGlobal("location", {
    search,
    get href() {
      return "";
    },
    set href(v: string) {
      href(v);
    },
    reload: vi.fn(),
  });
}

/**
 * Renders the app past its session gate. /api/me is the first request App
 * makes, so every test that wants to see the UI has to get through it first.
 */
async function renderSignedIn() {
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => [] })));
  render(<App />);
  await screen.findByRole("heading", { level: 1 });
}

describe("App", () => {
  it("renders the heading with text rongo", async () => {
    await renderSignedIn();
    const heading = screen.getByRole("heading", { level: 1 });
    expect(heading.textContent).toBe("rongo");
  });

  it("sends an expired login to the provider instead of showing the app", async () => {
    // Without this gate every single panel fails with its own 401 instead,
    // and the user sees a broken app rather than a sign-in.
    const href = vi.fn();
    stubLocation(href, "");
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 401, json: async () => ({}) })));

    render(<App />);

    await waitFor(() => expect(href).toHaveBeenCalledWith("/api/auth/login"));
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
  });

  it("still shows the app when /api/me fails on the network", async () => {
    // A network error is not an expired session. Redirecting here throws the
    // user at the provider on every hiccup of the backend.
    const href = vi.fn();
    stubLocation(href, "");
    vi.stubGlobal("fetch", vi.fn(async () => { throw new Error("offline"); }));

    render(<App />);

    await screen.findByRole("heading", { level: 1 });
    expect(href).not.toHaveBeenCalled();
  });

  it("halts after a failed callback instead of looping", async () => {
    // Without this gate: /api/me says 401, the UI goes to /api/auth/login, the
    // provider still has a session and answers without a prompt, the callback
    // fails again — a tight loop with no message at all. Two tabs opened at the
    // same time are enough to trigger it.
    const href = vi.fn();
    stubLocation(href, "?auth_error=oidc_callback_failed");
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, json: async () => ({}) }));
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);

    await screen.findByRole("link", { name: "Sign in" });
    expect(href).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("halts after sign-out instead of signing in again right away", async () => {
    // rongo only revokes its own session; the provider's stays. Redirecting
    // here gets a fresh token without a prompt and signs the user back in — the
    // sign-out button would visibly do nothing.
    const href = vi.fn();
    stubLocation(href, "?signed_out=1");
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 401, json: async () => ({}) })));

    render(<App />);

    await screen.findByRole("link", { name: "Sign in" });
    expect(href).not.toHaveBeenCalled();
  });

  it("does not show the signed-in app on a 5xx from /api/me", async () => {
    // A fully chromed app whose every panel then fails on its own tells the
    // user less than one clear message does.
    const href = vi.fn();
    stubLocation(href, "");
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 500, json: async () => ({}) })));

    render(<App />);

    await screen.findByText(/HTTP 500/);
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
  });

  it("signs out and follows the response's redirect_url", async () => {
    const href = vi.fn();
    stubLocation(href, "");
    const fetchMock = vi.fn(async (url: string) => ({
      ok: true,
      status: 200,
      json: async () =>
        String(url) === "/api/auth/logout" ? { redirect_url: "/?signed_out=1" } : [],
    }));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole("heading", { level: 1 });

    await user.click(screen.getByRole("button", { name: "Sign out" }));

    await waitFor(() => expect(href).toHaveBeenCalledWith("/?signed_out=1"));
    expect(fetchMock).toHaveBeenCalledWith("/api/auth/logout", { method: "POST" });
  });

  it("keeps the running thread when switching to Repos", async () => {
    // Unmounting Ask would drop the answer on screen while the stream keeps
    // writing into a dead component. The stored record only catches up once the
    // turn is finished, so a stream interrupted this way is lost for good.
    await renderSignedIn();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Question"), "A question that has to stay put");

    const pages = within(screen.getByRole("navigation", { name: "Pages" }));
    await user.click(pages.getByRole("button", { name: "Repos" }));
    await user.click(pages.getByRole("button", { name: "Ask" }));

    expect((screen.getByLabelText("Question") as HTMLTextAreaElement).value).toBe(
      "A question that has to stay put",
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

const oneThread = [{ id: 7, title: "How does shipping work?", created_at: "2026-08-17T10:00:00Z" }];
const oneTurn = [
  {
    id: 1,
    ordinal: 0,
    audience: "ba",
    question: "How does shipping work?",
    answer: "Through a job [1].",
    error: "",
    citations: [],
    created_at: "2026-08-17T10:00:00Z",
  },
];

describe("App, the thread across a reload", () => {
  it("brings back the thread that was last open", async () => {
    localStorage.setItem("rongo.thread", "7");
    apiFetch(oneThread, oneTurn);
    render(<StrictMode><App /></StrictMode>);
    expect(await screen.findByText(/Through a job/)).toBeTruthy();
  });

  it("remembers the chosen thread", async () => {
    apiFetch(oneThread, oneTurn);
    const user = userEvent.setup();
    render(<StrictMode><App /></StrictMode>);
    await user.click(await screen.findByRole("button", { name: "How does shipping work?" }));
    await waitFor(() => expect(localStorage.getItem("rongo.thread")).toBe("7"));
  });

  // A thread that is not yours, or was purged, comes back as an empty list with
  // status 200. Keeping the id would make every later reload open nothing.
  it("forgets a thread id that leads nowhere", async () => {
    localStorage.setItem("rongo.thread", "999");
    apiFetch([], []);
    render(<StrictMode><App /></StrictMode>);
    await waitFor(() => expect(localStorage.getItem("rongo.thread")).toBeNull());
  });
});
