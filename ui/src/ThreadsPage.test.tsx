import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { render, screen, waitFor, act, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import ThreadsPage, { mark, snippet, when } from "./ThreadsPage";

/**
 * The API, by URL: the paged list answers its envelope page by page, the
 * search answers its hits. Every call is recorded for the assertions.
 */
function api({
  pages = [[]],
  hits = [],
}: {
  pages?: { id: string; title: string; created_at: string }[][];
  hits?: { id: string; title: string; created_at: string; snippet?: string }[];
}) {
  const calls: string[] = [];
  const fetchMock = vi.fn(async (input: string | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(init?.method ? `${init.method} ${url}` : url);
    if (url.startsWith("/api/threads/search")) {
      return { ok: true, status: 200, json: async () => ({ items: hits }) };
    }
    if (url.startsWith("/api/threads?")) {
      const cursor = new URL(url, "http://x").searchParams.get("cursor");
      const n = cursor === null ? 0 : Number(cursor.slice("page".length));
      const items = pages[n] ?? [];
      const next = n + 1 < pages.length ? `page${n + 1}` : null;
      return { ok: true, status: 200, json: async () => ({ items, next_cursor: next }) };
    }
    return { ok: true, status: 204, json: async () => null };
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

/** IntersectionObserver as jsdom lacks it: the test decides when it fires. */
let observers: { cb: IntersectionObserverCallback }[] = [];
function stubObserver() {
  observers = [];
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      cb: IntersectionObserverCallback;
      constructor(cb: IntersectionObserverCallback) {
        this.cb = cb;
        observers.push(this);
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
}
function reveal() {
  for (const o of observers) {
    o.cb([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);
  }
}

function page(props: Partial<Parameters<typeof ThreadsPage>[0]> = {}) {
  return render(
    <ThreadsPage
      activeId={null}
      version={0}
      onSelect={() => {}}
      onChanged={() => {}}
      onDeleted={() => {}}
      {...props}
    />,
  );
}

const row = (id: string, title: string) => ({ id, title, created_at: "2026-08-17 10:00:00" });

beforeEach(stubObserver);
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("ThreadsPage", () => {
  it("lists the first page, fifty at a time", async () => {
    const calls = api({ pages: [[row("a", "First"), row("b", "Second")]] });
    page();
    expect(await screen.findByText("First")).toBeTruthy();
    expect(screen.getByText("Second")).toBeTruthy();
    expect(calls).toEqual(["/api/threads?limit=50"]);
  });

  it("loads the next page when the foot comes into view", async () => {
    const calls = api({ pages: [[row("a", "First")], [row("b", "Second")]] });
    page();
    await screen.findByText("First");
    expect(screen.queryByText("Second")).toBe(null);

    act(reveal);

    expect(await screen.findByText("Second")).toBeTruthy();
    expect(screen.getByText("First")).toBeTruthy();
    expect(calls).toEqual(["/api/threads?limit=50", "/api/threads?limit=50&cursor=page1"]);
    // The last page: nothing more to ask for.
    act(reveal);
    expect(calls).toHaveLength(2);
  });

  it("asks for a failed page once, not on every tick the foot stays in view", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        calls++;
        const cursor = new URL(String(input), "http://x").searchParams.get("cursor");
        if (cursor === null) {
          return { ok: true, status: 200, json: async () => ({ items: [row("a", "First")], next_cursor: "page1" }) };
        }
        return { ok: false, status: 500, json: async () => ({}) };
      }),
    );
    page();
    await screen.findByText("First");
    act(reveal);
    expect(await screen.findByRole("alert")).toBeTruthy();
    const afterFailure = calls;
    // The sentinel is still in view and the observer keeps saying so.
    act(reveal);
    act(reveal);
    await new Promise((r) => setTimeout(r, 20));
    expect(calls).toBe(afterFailure);
    expect(screen.getByText("First")).toBeTruthy();
  });

  it("says so when there is nothing yet", async () => {
    api({ pages: [[]] });
    page();
    expect(await screen.findByText("No threads yet.")).toBeTruthy();
  });

  it("searches once the typing has paused, and not per keystroke", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = api({ pages: [[row("a", "First")]], hits: [{ ...row("b", "The mail digest"), snippet: "sent by «mail» at night" }] });
    page();
    await screen.findByText("First");
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });

    await user.type(screen.getByLabelText("Search threads"), "mail");
    expect(calls.filter((c) => c.includes("/search"))).toHaveLength(0);
    await act(async () => {
      vi.advanceTimersByTime(300);
    });

    await waitFor(() => expect(calls.filter((c) => c.includes("/search"))).toEqual(["/api/threads/search?q=mail&limit=200"]));
    expect(await screen.findByText("The", { exact: false })).toBeTruthy();
    // The hit's title has the term in bold, and the passage is under it.
    expect(screen.getAllByText("mail", { selector: "strong" })).toHaveLength(2);
    expect(screen.getByText("sent by", { exact: false })).toBeTruthy();
    // The scrolled list is off the page while a term is in the box.
    expect(screen.queryByText("First")).toBe(null);
  });

  it("says so when nothing matches", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    api({ pages: [[row("a", "First")]], hits: [] });
    page();
    await screen.findByText("First");
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await user.type(screen.getByLabelText("Search threads"), "zzz");
    await act(async () => {
      vi.advanceTimersByTime(300);
    });
    expect(await screen.findByText("No thread matches.")).toBeTruthy();
  });

  it("opens the thread a row is clicked on", async () => {
    api({ pages: [[row("a", "First")]] });
    const onSelect = vi.fn();
    page({ onSelect });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "First" }));
    expect(onSelect).toHaveBeenCalledWith("a");
  });

  it("deletes from the row's menu and tells the shell", async () => {
    const calls = api({ pages: [[row("a", "First"), row("b", "Second")]] });
    const onDeleted = vi.fn();
    page({ onDeleted });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Actions for First" }));
    await user.click(screen.getByRole("menuitem", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(screen.queryByText("First")).toBe(null));
    expect(screen.getByText("Second")).toBeTruthy();
    expect(calls).toContain("DELETE /api/threads/a");
    expect(onDeleted).toHaveBeenCalledWith("a");
  });

  it("stars from the row's menu and keeps the row where it is", async () => {
    const calls = api({ pages: [[row("a", "First"), row("b", "Second")]] });
    const onChanged = vi.fn();
    page({ onChanged });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Actions for First" }));
    await user.click(screen.getByRole("menuitem", { name: "Star" }));

    await waitFor(() => expect(calls).toContain("POST /api/threads/a/star"));
    expect(onChanged).toHaveBeenCalled();
    // No sections here: the list is paged by cursor. The row stays, and its
    // menu now offers the way back.
    const titles = screen.getAllByRole("button", { name: /^(First|Second)$/ }).map((b) => b.textContent);
    expect(titles).toEqual(["First", "Second"]);
    await user.click(screen.getByRole("button", { name: "Actions for First" }));
    expect(screen.getByRole("menuitem", { name: "Unstar" })).toBeTruthy();
  });

  it("keeps its place when a row is renamed here, and reloads when the rail changes", async () => {
    const calls = api({ pages: [[row("a", "First")], [row("b", "Second")]] });
    const onChanged = vi.fn();
    const { rerender } = render(
      <ThreadsPage activeId={null} version={0} onSelect={() => {}} onChanged={onChanged} onDeleted={() => {}} />,
    );
    await screen.findByText("First");
    act(reveal);
    await screen.findByText("Second");
    const user = userEvent.setup();

    // A rename on this page: patched in place, the rail told, no reload.
    await user.click(screen.getByRole("button", { name: "Actions for Second" }));
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    await user.clear(within(screen.getByRole("dialog")).getByRole("textbox"));
    await user.type(within(screen.getByRole("dialog")).getByRole("textbox"), "Renamed");
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Save" }));
    await screen.findByText("Renamed");
    expect(onChanged).toHaveBeenCalledTimes(1);
    const before = calls.filter((c) => c.startsWith("/api/threads?")).length;
    rerender(
      <ThreadsPage activeId={null} version={1} onSelect={() => {}} onChanged={onChanged} onDeleted={() => {}} />,
    );
    await waitFor(() => expect(screen.getByText("Renamed")).toBeTruthy());
    expect(calls.filter((c) => c.startsWith("/api/threads?"))).toHaveLength(before);
    expect(screen.getByText("First")).toBeTruthy();

    // A change made elsewhere: page one again.
    rerender(
      <ThreadsPage activeId={null} version={2} onSelect={() => {}} onChanged={onChanged} onDeleted={() => {}} />,
    );
    await waitFor(() => expect(calls.filter((c) => c.startsWith("/api/threads?"))).toHaveLength(before + 1));
    await waitFor(() => expect(screen.queryByText("Renamed")).toBe(null));
  });

  it("reports when the list cannot be fetched", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 500, json: async () => ({}) })));
    page();
    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});

describe("the row's words", () => {
  it("dates a row by its day, with the year only when it is not this one", () => {
    const now = new Date("2026-09-13T12:00:00Z");
    expect(when("2026-08-17 10:00:00", now)).toBe("17 Aug");
    expect(when("2025-08-17 10:00:00", now)).toBe("17 Aug 2025");
    expect(when("nope", now)).toBe("");
  });

  it("bolds every term of the search in a title", () => {
    const { container } = render(<>{mark("Mail and the mailer", "mail the")}</>);
    expect(container.querySelectorAll("strong").length).toBe(3);
    expect(container.textContent).toBe("Mail and the mailer");
    // A term that is regex syntax is a character.
    expect(render(<>{mark("a+b", "a+")}</>).container.querySelector("strong")?.textContent).toBe("a+");
    expect(render(<>{mark("plain", "  ")}</>).container.textContent).toBe("plain");
  });

  it("bolds the passage's matches and cuts a long lead at a word", () => {
    const { container } = render(<>{snippet("one two three four five six seven eight nine ten «hit» after")}</>);
    expect(container.querySelector("strong")?.textContent).toBe("hit");
    expect(container.textContent?.startsWith("…")).toBe(true);
    expect(container.textContent).toContain("ten hit after");
    expect(render(<>{snippet("short «hit»")}</>).container.textContent).toBe("short hit");
  });
});
