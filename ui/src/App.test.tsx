import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";
import { routeFromPath } from "./routing";

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
  window.history.replaceState({}, "", "/");
});

/**
 * The address bar is where the app is now, so a test that wants a thread open
 * says so the way a reader would: by being at its URL.
 */
function atPath(path: string) {
  window.history.replaceState({}, "", path);
}

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
  it("renders the heading with text Rongo", async () => {
    await renderSignedIn();
    const heading = screen.getByRole("heading", { level: 1 });
    expect(heading.textContent).toBe("Rongo");
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

  it("says only 'signed out' after a password-mode sign-out", async () => {
    // No provider, so no "sign out there as well".
    const href = vi.fn();
    stubLocation(href, "?signed_out=local");
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 401, json: async () => ({}) })));

    render(<App />);

    await screen.findByRole("link", { name: "Sign in" });
    expect(screen.getByText("Signed out.")).toBeTruthy();
    expect(screen.queryByText(/provider/)).toBeNull();
  });

  describe("password login", () => {
    function renderForm(status: number) {
      const href = vi.fn();
      stubLocation(href, "?login=password");
      const fetchMock = vi.fn(async () => ({ ok: status < 300, status, json: async () => ({}) }));
      vi.stubGlobal("fetch", fetchMock);
      render(<App />);
      return { href, fetchMock };
    }

    it("shows the form without asking /api/me first", async () => {
      // /api/me would 401 and send the browser straight back to the login
      // route: a loop with a form flashing in the middle.
      const { href, fetchMock } = renderForm(204);

      await screen.findByRole("form", { name: "Sign in" });

      expect(fetchMock).not.toHaveBeenCalled();
      expect(href).not.toHaveBeenCalled();
      expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
    });

    it("posts the credentials and reloads the app on success", async () => {
      const { href, fetchMock } = renderForm(204);
      const user = userEvent.setup();
      await screen.findByRole("form", { name: "Sign in" });

      await user.type(screen.getByLabelText("Username"), "admin");
      await user.type(screen.getByLabelText("Password"), "hunter2");
      await user.click(screen.getByRole("button", { name: "Sign in" }));

      await waitFor(() => expect(href).toHaveBeenCalledWith("/"));
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/auth/password",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({ username: "admin", password: "hunter2" }),
        }),
      );
    });

    it("shows one generic error on a 401 and stays on the form", async () => {
      const { href } = renderForm(401);
      const user = userEvent.setup();
      await screen.findByRole("form", { name: "Sign in" });

      await user.type(screen.getByLabelText("Username"), "admin");
      await user.type(screen.getByLabelText("Password"), "wrong");
      await user.click(screen.getByRole("button", { name: "Sign in" }));

      await screen.findByRole("alert");
      expect(screen.getByRole("alert").textContent).toBe("Wrong username or password.");
      expect(href).not.toHaveBeenCalled();
      expect(screen.getByRole("form", { name: "Sign in" })).toBeTruthy();
    });
  });

  it("reloads in place when an authenticating proxy answers /api/me with its sign-in page", async () => {
    // oauth-proxy in front of rongo: an expired proxy cookie turns every
    // request into a 403 HTML page. Only a navigation can show that page,
    // and reloading this URL rather than going to the root is what lets the
    // proxy return the user to where they were.
    const href = vi.fn();
    stubLocation(href, "");
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: false,
        status: 403,
        headers: new Headers({ "content-type": "text/html; charset=utf-8" }),
        json: async () => ({}),
      })),
    );

    render(<App />);

    await waitFor(() => expect(window.location.reload).toHaveBeenCalled());
    expect(href).not.toHaveBeenCalled();
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
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

  it("opens the Threads page from the rail and marks it current", async () => {
    await renderSignedIn();
    const user = userEvent.setup();

    const threads = screen.getByRole("button", { name: "Threads" });
    await user.click(threads);

    expect(window.location.pathname).toBe("/threads");
    expect(threads.getAttribute("aria-current")).toBe("page");
    expect(screen.getByLabelText("Search threads")).toBeTruthy();
    // The header names the page.
    expect(screen.getByRole("banner").textContent).toContain("Threads");
  });

  it("keeps the running thread when switching to Projects", async () => {
    // Unmounting Ask would drop the answer on screen while the stream keeps
    // writing into a dead component. The stored record only catches up once the
    // turn is finished, so a stream interrupted this way is lost for good.
    await renderSignedIn();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Question"), "A question that has to stay put");

    const repos = screen.getByRole("button", { name: "Projects" });
    await user.click(repos);
    expect(repos.getAttribute("aria-current")).toBe("page");
    // With the page nav gone, the rail is the way back: New question here,
    // because this draft belongs to no thread yet. With a thread open it is
    // that thread's row, which stays clickable even mid-stream.
    await user.click(screen.getByRole("button", { name: "New question" }));
    expect(repos.getAttribute("aria-current")).toBe(null);

    expect((screen.getByLabelText("Question") as HTMLTextAreaElement).value).toBe(
      "A question that has to stay put",
    );
  });

  // The action used to live inside the thread list, among the past
  // questions, which read as if starting one were already history.
  it("clears the open thread from the rail's New question", async () => {
    atPath("/thread/" + addr);
    apiFetch(oneThread, []);
    render(<App />);
    await screen.findByRole("heading", { level: 1 });
    await screen.findAllByText("How does shipping work?");
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "New question" }));

    expect(window.location.pathname).toBe("/new");
    // The header names the open thread; with none open it falls back.
    expect(screen.getAllByText("New question").length).toBeGreaterThan(1);
  });

  // "Threads /" pointed at nothing you could click once the nav went.
  it("heads the answer with the thread title alone", async () => {
    atPath("/thread/" + addr);
    apiFetch(oneThread, []);
    render(<App />);
    await screen.findByRole("heading", { level: 1 });

    await screen.findAllByText("How does shipping work?");
    // No "Threads /" breadcrumb root in the header. The word is in the rail
    // now, where it is a page, not a root the title hangs off.
    expect(screen.getByRole("banner").textContent).not.toContain("Threads");
  });
});

/** A real thread address: 22 URL-safe characters, what the store mints. */
const addr = "v76BBy2b1nMYOFl2Lnm9JQ";
/** A second one, for the address that leads nowhere. */
const noSuchAddr = "AAAAAAAAAAAAAAAAAAAAAA";

/** Answers the thread list and one thread's turns separately. */
function apiFetch(threads: unknown, messages: unknown) {
  const mock = vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () =>
      String(url).startsWith("/api/threads/") ? messages : { items: threads, next_cursor: null },
  }));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const oneThread = [{ id: addr, title: "How does shipping work?", created_at: "2026-08-17T10:00:00Z" }];
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

describe("App, the thread in the URL", () => {
  it("opens the thread its address names", async () => {
    atPath("/thread/" + addr);
    apiFetch(oneThread, oneTurn);
    render(<StrictMode><App /></StrictMode>);
    expect(await screen.findByText(/Through a job/)).toBeTruthy();
  });

  it("puts the thread in the address bar when one is opened", async () => {
    // The whole point of the URL: a thread can be sent to someone, reloaded
    // into, and reached with Back.
    apiFetch(oneThread, oneTurn);
    const user = userEvent.setup();
    render(<StrictMode><App /></StrictMode>);
    await user.click(await screen.findByRole("button", { name: "How does shipping work?" }));
    await waitFor(() => expect(window.location.pathname).toBe("/thread/" + addr));
  });

  it("names the unasked question /new rather than /", async () => {
    atPath("/");
    apiFetch([], []);
    render(<StrictMode><App /></StrictMode>);
    await screen.findByRole("heading", { level: 1 });
    await waitFor(() => expect(window.location.pathname).toBe("/new"));
  });

  it("follows Back out of a thread", async () => {
    apiFetch(oneThread, oneTurn);
    const user = userEvent.setup();
    render(<StrictMode><App /></StrictMode>);
    await user.click(await screen.findByRole("button", { name: "How does shipping work?" }));
    await waitFor(() => expect(window.location.pathname).toBe("/thread/" + addr));

    window.history.back();

    await waitFor(() => expect(window.location.pathname).toBe("/new"));
    await waitFor(() => expect(screen.getAllByText("New question").length).toBeGreaterThan(1));
  });

  // The placeholder title is the question's first 48 runes, cut mid-word. It
  // is a label for a rail row, never a title, and the header showing it cut it
  // a second time with its own truncate.
  it("holds New question in the header while the title is still coming", async () => {
    atPath("/thread/" + addr);
    apiFetch(
      [{ id: addr, title: "How does shipping work, and what happens when…", title_pending: true, created_at: "2026-08-17T10:00:00Z" }],
      oneTurn,
    );
    render(<StrictMode><App /></StrictMode>);
    await screen.findByRole("heading", { level: 1 });

    // The rail keeps the placeholder — there the first words are what tells
    // one pending row from another — and the header does not.
    await screen.findByRole("button", { name: "How does shipping work, and what happens when…" });
    expect(screen.queryAllByText("How does shipping work, and what happens when…").length).toBe(1);
    expect(screen.getAllByText("New question").length).toBeGreaterThan(1);
  });

  it("puts the title in the header once it has settled", async () => {
    atPath("/thread/" + addr);
    apiFetch([{ id: addr, title: "Shipping, end to end", title_pending: false, created_at: "2026-08-17T10:00:00Z" }], oneTurn);
    render(<StrictMode><App /></StrictMode>);

    // Twice: the rail row and the header.
    await waitFor(() => expect(screen.getAllByText("Shipping, end to end").length).toBe(2));
  });

  // The rail is the latest 30, and a thread opened by its address can be any
  // age. The header asks for that one row on its own rather than showing
  // "New question" over a conversation that has a name.
  it("names a thread the rail does not carry", async () => {
    atPath("/thread/" + addr);
    const mock = vi.fn(async (url: string) => {
      const u = String(url);
      const body = u.endsWith("/summary")
        ? { id: addr, title: "Older than the rail", title_pending: false, created_at: "2025-01-01 10:00:00" }
        : u.startsWith("/api/threads/")
          ? oneTurn
          : { items: [], next_cursor: null };
      return { ok: true, status: 200, json: async () => body };
    });
    vi.stubGlobal("fetch", mock);
    render(<StrictMode><App /></StrictMode>);

    // Once: the header, with no rail row to go with it.
    await waitFor(() => expect(screen.getAllByText("Older than the rail").length).toBe(1));
    expect(mock).toHaveBeenCalledWith(`/api/threads/${addr}/summary`);
  });

  // A thread that is not yours, or was purged, comes back as an empty list
  // with status 200; an address that names none at all comes back 404. Both
  // say so on the page. Falling back to the composer would be a soft 404: the
  // reader's link "worked", and the address in the bar would go on lying.
  it("says so when a thread address leads nowhere, and keeps the address", async () => {
    atPath("/thread/" + noSuchAddr);
    apiFetch([], []);
    render(<StrictMode><App /></StrictMode>);

    expect(await screen.findByText(/no longer available/)).toBeTruthy();
    expect(window.location.pathname).toBe("/thread/" + noSuchAddr);
  });

  // A shape that is not an address never reaches the app at all: the server
  // answers 404 for it (see backend/web isRoute), so there is nothing here to
  // fall back from. What the client still has to answer for is an address of
  // the right shape that names no thread, which is the test above.
  it("does not treat a row number as a thread", () => {
    expect(routeFromPath("/thread/19")).toEqual({ view: "new" });
  });
});

describe("App, the rail on a phone", () => {
  // Below lg the 300px rail is an off-canvas drawer: at 390px it would
  // otherwise leave the thread 90px, which is what made the app unusable on a
  // phone. jsdom evaluates no media query, so every assertion here is on the
  // state-driven class string or on behaviour, never on layout.
  // By id, not by role: Ask's Sources pane is a landmark too, so
  // getByRole("complementary") finds two.
  const rail = () => document.getElementById("nav-drawer") as HTMLElement;

  async function openDrawer() {
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Open navigation" }));
    return user;
  }

  it("keeps the rail off-canvas until it is asked for", async () => {
    await renderSignedIn();
    expect(rail().className).toContain("-translate-x-full");
    expect(rail().className).toContain("lg:translate-x-0");
    expect(screen.getByRole("button", { name: "Open navigation" }).className).toContain("lg:hidden");
  });

  it("slides the rail in and lays a backdrop over the thread", async () => {
    await renderSignedIn();
    await openDrawer();
    expect(rail().className).toContain("translate-x-0");
    expect(rail().className).not.toContain("-translate-x-full");
    expect(document.querySelector(".bg-black\\/50")).toBeTruthy();
  });

  it("closes on the backdrop, which is the way out with no close button", async () => {
    await renderSignedIn();
    const user = await openDrawer();
    await user.click(document.querySelector(".bg-black\\/50") as HTMLElement);
    expect(rail().className).toContain("-translate-x-full");
  });

  it("closes on Escape", async () => {
    await renderSignedIn();
    const user = await openDrawer();
    await user.keyboard("{Escape}");
    expect(rail().className).toContain("-translate-x-full");
  });

  // A rail parked off-screen still takes tab stops and still reads to a
  // screen reader: tabbing past the toggle on a phone walked invisibly
  // through New question, every thread row and Projects. invisible takes it out
  // of both, and lg:visible puts it back where the rail is the layout.
  it("keeps the closed rail out of the tab order and the a11y tree", async () => {
    await renderSignedIn();
    expect(rail().className).toContain("invisible");
    expect(rail().className).toContain("lg:visible");
    const user = await openDrawer();
    expect(rail().className).toContain("visible");
    expect(rail().className).not.toContain("invisible");
    await user.keyboard("{Escape}");
    expect(rail().className).toContain("invisible");
  });

  it("closes when a thread is picked", async () => {
    apiFetch(oneThread, oneTurn);
    render(<App />);
    await screen.findByRole("heading", { level: 1 });
    const user = await openDrawer();
    await user.click(await screen.findByRole("button", { name: "How does shipping work?" }));
    expect(rail().className).toContain("-translate-x-full");
  });

  it("closes on New question", async () => {
    await renderSignedIn();
    const user = await openDrawer();
    await user.click(screen.getByRole("button", { name: "New question" }));
    expect(rail().className).toContain("-translate-x-full");
  });

  it("closes on Projects", async () => {
    await renderSignedIn();
    const user = await openDrawer();
    await user.click(screen.getByRole("button", { name: "Projects" }));
    expect(rail().className).toContain("-translate-x-full");
    await screen.findByRole("heading", { name: "Projects" });
  });

  it("closes on Shared", async () => {
    await renderSignedIn();
    const user = await openDrawer();
    const shared = screen.getByRole("button", { name: "Shared" });
    await user.click(shared);
    expect(rail().className).toContain("-translate-x-full");
    expect(shared.getAttribute("aria-current")).toBe("page");
    await screen.findByRole("heading", { name: "Shared threads" });
  });

  // The rail rows are disabled mid-turn; the way back to the rail must not be.
  // With the drawer shut and the toggle dead there would be no navigation at
  // all while an answer is being written.
  it("leaves the toggle usable while a turn is running", async () => {
    await renderSignedIn();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Question"), "A question");
    expect(screen.getByRole("button", { name: "Open navigation" }).hasAttribute("disabled")).toBe(
      false,
    );
  });

  // The cramped phone header hides the wordmark visually, never from the
  // accessibility tree: the h1 is how a reader and every test above tell
  // "signed in" from "not yet". `hidden` would keep jsdom green and break it
  // in a browser.
  it("keeps the wordmark readable to a screen reader when it is out of sight", async () => {
    await renderSignedIn();
    const h1 = screen.getByRole("heading", { level: 1 });
    expect(h1.textContent).toBe("Rongo");
    expect(h1.className).toContain("sr-only");
    expect(h1.className).toContain("sm:not-sr-only");
  });
});
