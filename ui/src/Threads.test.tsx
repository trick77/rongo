import { describe, it, expect, vi, afterEach, type Mock } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Threads from "./Threads";

function threadList(list: unknown, ok = true, starred: unknown[] = []) {
  // The list comes in its envelope, as the API sends it. The rail reads two:
  // the recent page and the starred list, told apart by the query.
  const envelope = (l: unknown) => (Array.isArray(l) ? { items: l, next_cursor: null } : l);
  const body = envelope(list);
  const stars = envelope(starred);
  const fetchMock = vi.fn(async (url: string) => ({
    ok,
    status: ok ? 200 : 500,
    json: async () => (url.includes("starred=true") ? stars : body),
  }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

/** The two loads the rail makes, so a count of the requests after them starts at 0. */
const loads = 2;

afterEach(() => vi.unstubAllGlobals());

const two = [
  { id: "7", title: "How does shipping work?", created_at: "2026-08-17T10:00:00Z" },
  { id: "3", title: "Where does the token come from?", created_at: "2026-08-16T10:00:00Z" },
];

describe("Threads", () => {
  it("loads the list and shows the titles", async () => {
    threadList(two);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    expect(await screen.findByText("How does shipping work?")).toBeTruthy();
    expect(screen.getByText("Where does the token come from?")).toBeTruthy();
  });

  it("fades a long title out instead of cutting it with an ellipsis", async () => {
    // As ../loom does: the title runs under a gradient to the row's own
    // background. The text stays whole for a reader and a test.
    threadList(two);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    const title = await screen.findByText("How does shipping work?");
    expect(title.className).not.toContain("truncate");
    expect(title.className).toContain("whitespace-nowrap");
    const fade = title.querySelector("[aria-hidden]");
    expect(fade?.className).toContain("bg-gradient-to-r");
  });

  it("marks the open thread", async () => {
    threadList(two);
    render(<Threads activeId="3" onSelect={() => {}} version={0} />);
    const active = await screen.findByRole("button", { name: "Where does the token come from?" });
    expect(active.getAttribute("aria-current")).toBe("true");
    expect(
      screen.getByRole("button", { name: "How does shipping work?" }).getAttribute("aria-current"),
    ).toBeNull();
  });

  it("passes the choice upwards", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId={null} onSelect={onSelect} version={0} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Where does the token come from?" }));
    expect(onSelect).toHaveBeenCalledWith("3");
  });

  // The model-written title replaces the placeholder in a background goroutine
  // with no way to push it. Without a reload the sidebar shows the truncated
  // question until someone reloads the page.
  it("reloads when the version changes", async () => {
    const fetchMock = threadList(two);
    const { rerender } = render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    await screen.findByText("How does shipping work?");
    rerender(<Threads activeId={null} onSelect={() => {}} version={1} />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2 * loads));
  });

  // A list that cannot be loaded is not an error banner: the rail keeps its
  // shape and asking a new question, which lives above this component, still
  // works.
  it("stays quiet when the server refuses the list", async () => {
    threadList(null, false);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    const list = await screen.findByRole("navigation", { name: "Threads" });
    expect(within(list).queryAllByRole("button")).toHaveLength(0);
  });

  // A turn being written no longer closes the rail. The answer is parked on
  // its own thread and goes on arriving there, so reading something else
  // while it lands costs nothing.
  it("opens another thread while an answer is streaming", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId="7" onSelect={onSelect} version={0} busy busyId="7" />);
    const other = await screen.findByRole("button", { name: "Where does the token come from?" });
    expect((other as HTMLButtonElement).disabled).toBe(false);
    const user = userEvent.setup();
    await user.click(other);
    expect(onSelect).toHaveBeenCalledWith("3");
  });

  // With the page nav gone, this row is the only way back to a streaming
  // answer from the Repos page.
  it("keeps the running thread's own row clickable while it streams", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId="7" onSelect={onSelect} version={0} busy busyId="7" />);
    const own = await screen.findByRole("button", { name: "How does shipping work?" });
    expect((own as HTMLButtonElement).disabled).toBe(false);
    const user = userEvent.setup();
    await user.click(own);
    expect(onSelect).toHaveBeenCalledWith("7");
  });

  // The 28px row is the pitch on every pointer: a touch screen once got 44px,
  // and the rail's own spacing, tuned against 28, stayed standing around it.
  // The height sits on the row that holds the title and the actions button,
  // not on the title alone.
  it("keeps the desktop row height on a touch screen", async () => {
    threadList(two);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    const title = await screen.findByRole("button", { name: "How does shipping work?" });
    expect(title.parentElement?.className).toContain("h-7");
    expect(title.parentElement?.className).not.toContain("pointer-coarse:");
  });

  // The whole of that 28px has to be tappable. The title button is a flex
  // child under items-center, so without self-stretch it shrinks to its 20px
  // line box and leaves a dead 4px band along the top and bottom of every
  // row — which on the topmost row, under 20px of the group's own margin, is
  // a tap that lands on nothing at all.
  it("gives the title the whole height of its row to be tapped in", async () => {
    threadList(two);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    const title = await screen.findByRole("button", { name: "How does shipping work?" });
    expect(title.className).toContain("self-stretch");
    // And the actions button reaches the same 28px without its 24px hover
    // square growing with it.
    const actions = screen.getAllByRole("button", { name: /^Actions for/ })[0];
    expect(actions.className).toContain("h-6");
    expect(actions.className).toContain("after:-inset-y-0.5");
  });

  // Today's threads head the list, so a "Today" heading names what the
  // position already says. The day groups below it stay: there the day is
  // the useful part.
  describe("day groups", () => {
    const days = (n: number) => new Date(Date.now() - n * 86400000).toISOString();
    const mixed = [
      { id: "9", title: "Asked this morning", created_at: days(0) },
      { id: "4", title: "Asked earlier in the week", created_at: days(3) },
    ];

    it("carries no heading for today", async () => {
      threadList(mixed);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      await screen.findByRole("button", { name: "Asked this morning" });
      expect(screen.queryByText("Today")).toBeNull();
    });

    it("leaves the older group its heading", async () => {
      threadList(mixed);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      await screen.findByRole("button", { name: "Asked earlier in the week" });
      expect(screen.getByText("This week")).toBeTruthy();
    });

    // The date used to sit at the end of every older row. The row menu took
    // its place: which thread this is, is the title's job, and what can be
    // done to it is the only other thing the row has to say.
    it("shows no date on any row", async () => {
      threadList(mixed);
      const { container } = render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      await screen.findByRole("button", { name: "Asked this morning" });
      expect(container.querySelectorAll("time").length).toBe(0);
    });
  });

  // ../loom's sections: the starred threads first under their own heading,
  // then the recent ones under "Recents". Starred is read separately, so a
  // star holds on a thread the 30 newest no longer carry.
  describe("sections", () => {
    const days = (n: number) => new Date(Date.now() - n * 86400000).toISOString();
    const older = { id: "1", title: "Asked long ago", created_at: days(40), starred: true };

    it("heads the recent threads and files the starred ones above them", async () => {
      threadList(two, true, [{ ...two[1], starred: true }, older]);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      const nav = await screen.findByRole("navigation", { name: "Threads" });
      const starred = within(nav).getByText("Starred");
      const recents = within(nav).getByText("Recents");
      expect(starred.compareDocumentPosition(recents) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

      // The starred section carries the old thread the page does not.
      const starredRows = within(starred.parentElement as HTMLElement).getAllByRole("button", { name: /^(?!Actions)/ });
      expect(starredRows.map((b) => b.textContent)).toEqual(["Where does the token come from?", "Asked long ago"]);
      // And a starred thread is not listed a second time under Recents.
      const recentRows = within(recents.parentElement as HTMLElement).getAllByRole("button", { name: /^(?!Actions)/ });
      expect(recentRows.map((b) => b.textContent)).toEqual(["How does shipping work?"]);
    });

    it("shows no Starred heading until there is a star", async () => {
      threadList(two);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      await screen.findByText("Recents");
      expect(screen.queryByText("Starred")).toBeNull();
    });

    it("shows no Recents heading when every recent thread is starred", async () => {
      const starred = { id: "1", title: "Starred only", created_at: days(0), starred: true };
      threadList([starred], true, [starred]);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      await screen.findByText("Starred");
      expect(screen.queryByText("Recents")).toBeNull();
    });

    it("keeps the day groups under Recents, the first one on the heading's own gap", async () => {
      threadList([
        { id: "9", title: "Asked this morning", created_at: days(0) },
        { id: "4", title: "Asked earlier in the week", created_at: days(3) },
      ]);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      await screen.findByText("Recents");
      expect(screen.queryByText("Today")).toBeNull();
      expect(screen.getByText("This week").className).toContain("mt-5");

      // With nothing from today, the first day label sits on the heading's
      // 8px, not 20px further down.
      threadList([{ id: "4", title: "Asked earlier in the week", created_at: days(3) }]);
      render(<Threads activeId={null} onSelect={() => {}} version={1} />);
      await waitFor(() => expect(screen.getAllByText("This week")).toHaveLength(2));
      expect(screen.getAllByText("This week")[1].className).not.toContain("mt-5");
    });

    it("asks for every starred thread, not a page of them", async () => {
      const fetchMock = threadList(two);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      await screen.findByText("Recents");
      expect(fetchMock).toHaveBeenCalledWith("/api/threads?starred=true&limit=1000");
    });

    it("reports the union of both lists upwards", async () => {
      const onList = vi.fn();
      threadList(two, true, [{ ...two[1], starred: true }, older]);
      render(<Threads activeId={null} onSelect={() => {}} version={0} onList={onList} />);
      await screen.findByText("Starred");
      expect(onList.mock.calls[0][0].map((t: { id: string }) => t.id)).toEqual(["7", "3", "1"]);
    });
  });

  describe("the row menu", () => {
    /** Opens the menu on one row and hands back the user-event session. */
    async function openMenu(title: string) {
      const user = userEvent.setup();
      await user.click(await screen.findByRole("button", { name: "Actions for " + title }));
      return user;
    }

    it("offers Star first, and moves the row under Starred on the spot", async () => {
      threadList(two);
      const onStarred = vi.fn();
      render(<Threads activeId={null} onSelect={() => {}} version={0} onStarred={onStarred} />);
      const user = await openMenu("Where does the token come from?");
      const items = screen.getAllByRole("menuitem");
      expect(items[0]).toBe(screen.getByRole("menuitem", { name: "Star" }));

      (fetch as unknown as Mock).mockResolvedValueOnce({ ok: true, status: 204 });
      await user.click(items[0]);

      expect(fetch).toHaveBeenCalledWith("/api/threads/3/star", { method: "POST" });
      expect(onStarred).toHaveBeenCalled();
      const starred = await screen.findByText("Starred");
      expect(within(starred.parentElement as HTMLElement).getByRole("button", { name: "Where does the token come from?" })).toBeTruthy();
      // Once, not twice: the row left Recents.
      expect(screen.getAllByRole("button", { name: "Where does the token come from?" })).toHaveLength(1);

      // And back: the entry now reads Unstar and takes the row out again.
      await openMenu("Where does the token come from?");
      const unstar = screen.getAllByRole("menuitem")[0];
      expect(unstar).toBe(screen.getByRole("menuitem", { name: "Unstar" }));
      (fetch as unknown as Mock).mockResolvedValueOnce({ ok: true, status: 204 });
      await user.click(unstar);
      expect(fetch).toHaveBeenCalledWith("/api/threads/3/unstar", { method: "POST" });
      await waitFor(() => expect(screen.queryByText("Starred")).toBeNull());
      expect(screen.getByRole("button", { name: "Where does the token come from?" })).toBeTruthy();
    });

    it("leaves the star where it was when the server refuses", async () => {
      threadList(two);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      const user = await openMenu("Where does the token come from?");
      (fetch as unknown as Mock).mockResolvedValueOnce({ ok: false, status: 500 });
      await user.click(screen.getByRole("menuitem", { name: "Star" }));
      await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/threads/3/star", { method: "POST" }));
      expect(screen.queryByText("Starred")).toBeNull();
    });

    it("offers rename and delete, and closes on a click outside", async () => {
      threadList(two);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      const user = await openMenu("How does shipping work?");

      expect(screen.getByRole("menuitem", { name: "Rename" })).toBeTruthy();
      expect(screen.getByRole("menuitem", { name: "Delete" })).toBeTruthy();

      await user.click(document.body);
      expect(screen.queryByRole("menu")).toBeNull();
    });

    // Switching thread from under an open menu would leave it hanging off the
    // row that was just left, pointing at a thread nobody is looking at.
    it("closes when another row is picked", async () => {
      threadList(two);
      const onSelect = vi.fn();
      render(<Threads activeId={null} onSelect={onSelect} version={0} />);
      const user = await openMenu("How does shipping work?");

      await user.click(screen.getByRole("button", { name: "Where does the token come from?" }));

      expect(onSelect).toHaveBeenCalledWith("3");
      expect(screen.queryByRole("menu")).toBeNull();
    });

    // A menu already open when the question is sent has to go with the
    // trigger: left standing, it still offers Delete on the thread the
    // answer is landing on.
    it("closes when a turn starts under it", async () => {
      threadList(two);
      const { rerender } = render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      await openMenu("How does shipping work?");
      expect(screen.getByRole("menu")).toBeTruthy();

      rerender(<Threads activeId="7" onSelect={() => {}} version={0} busy busyId="7" />);

      expect(screen.queryByRole("menu")).toBeNull();
    });

    // Deleting the thread being written would pull the record out from under
    // the answer still landing on it. Only that one: every other row keeps
    // its actions, because nothing is being written into it.
    it("is withheld from the thread being written, and from no other", async () => {
      threadList(two);
      render(<Threads activeId="7" onSelect={() => {}} version={0} busy busyId="7" />);
      await screen.findByRole("button", { name: "How does shipping work?" });
      expect(screen.queryByRole("button", { name: "Actions for How does shipping work?" })).toBeNull();
      expect(screen.getByRole("button", { name: "Actions for Where does the token come from?" })).toBeTruthy();
    });

    // The reader has walked away from the answer and is reading another
    // thread: the row they are ON keeps its actions, and the one being
    // written — wherever it sits — does not.
    it("follows the thread being written, not the one on screen", async () => {
      threadList(two);
      render(<Threads activeId="3" onSelect={() => {}} version={0} busy busyId="7" />);
      await screen.findByRole("button", { name: "How does shipping work?" });
      expect(screen.queryByRole("button", { name: "Actions for How does shipping work?" })).toBeNull();
      expect(screen.getByRole("button", { name: "Actions for Where does the token come from?" })).toBeTruthy();
    });

    it("deletes the thread once the dialog is confirmed, and not before", async () => {
      threadList(two);
      const onDeleted = vi.fn();
      render(<Threads activeId={null} onSelect={() => {}} version={0} onDeleted={onDeleted} />);
      const user = await openMenu("How does shipping work?");

      await user.click(screen.getByRole("menuitem", { name: "Delete" }));
      expect(fetch).toHaveBeenCalledTimes(loads); // the list loads, and nothing more

      (fetch as unknown as Mock).mockResolvedValueOnce({ ok: true, status: 204 });
      await user.click(screen.getByRole("button", { name: "Delete" }));

      expect(fetch).toHaveBeenCalledWith("/api/threads/7", { method: "DELETE" });
      expect(onDeleted).toHaveBeenCalledWith("7");
      expect(screen.queryByRole("button", { name: "How does shipping work?" })).toBeNull();
    });

    it("leaves the row alone when the delete is cancelled", async () => {
      threadList(two);
      render(<Threads activeId={null} onSelect={() => {}} version={0} />);
      const user = await openMenu("How does shipping work?");

      await user.click(screen.getByRole("menuitem", { name: "Delete" }));
      await user.click(screen.getByRole("button", { name: "Cancel" }));

      expect(screen.queryByRole("dialog")).toBeNull();
      expect(screen.getByRole("button", { name: "How does shipping work?" })).toBeTruthy();
    });

    it("writes the typed title and shows it", async () => {
      threadList(two);
      const onRenamed = vi.fn();
      render(<Threads activeId={null} onSelect={() => {}} version={0} onRenamed={onRenamed} />);
      const user = await openMenu("How does shipping work?");

      await user.click(screen.getByRole("menuitem", { name: "Rename" }));
      const box = screen.getByRole("textbox", { name: "Thread title" });
      await user.clear(box);
      await user.type(box, "Shipping, end to end");
      (fetch as unknown as Mock).mockResolvedValueOnce({ ok: true, status: 204 });
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(fetch).toHaveBeenCalledWith("/api/threads/7", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title: "Shipping, end to end" }),
      });
      expect(onRenamed).toHaveBeenCalled();
      expect(screen.getByRole("button", { name: "Shipping, end to end" })).toBeTruthy();
    });

    // A row dropped from a delete the server refused would be a lie: the
    // thread is still there on the next reload.
    it("keeps the row and the dialog when the delete fails", async () => {
      threadList(two);
      const onDeleted = vi.fn();
      render(<Threads activeId={null} onSelect={() => {}} version={0} onDeleted={onDeleted} />);
      const user = await openMenu("How does shipping work?");

      await user.click(screen.getByRole("menuitem", { name: "Delete" }));
      (fetch as unknown as Mock).mockResolvedValueOnce({ ok: false, status: 500 });
      await user.click(screen.getByRole("button", { name: "Delete" }));

      expect(onDeleted).not.toHaveBeenCalled();
      expect(screen.getByRole("dialog")).toBeTruthy();
      expect(screen.getByRole("button", { name: "How does shipping work?" })).toBeTruthy();
    });
  });
});

describe("Threads, the latest 30", () => {
  it("asks for the rail's page, not the whole history", async () => {
    const fetchMock = threadList(two);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    await screen.findByText("How does shipping work?");
    expect(fetchMock).toHaveBeenCalledWith("/api/threads?limit=30");
  });

  it("opens the Threads page from the foot of the list", async () => {
    threadList(two);
    const onAllThreads = vi.fn();
    render(<Threads activeId={null} onSelect={() => {}} version={0} onAllThreads={onAllThreads} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "All threads" }));
    expect(onAllThreads).toHaveBeenCalledTimes(1);
    // A door, not a place: never marked current.
    expect(screen.getByRole("button", { name: "All threads" }).getAttribute("aria-current")).toBe(null);
  });

  it("has no foot under an empty list", async () => {
    threadList([]);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    const list = await screen.findByRole("navigation", { name: "Threads" });
    await waitFor(() => expect(within(list).queryByRole("button", { name: "All threads" })).toBe(null));
  });

  it("reads an answer that is not the envelope as an empty list", async () => {
    // Anything but the envelope — an error page, a stub answering [] to
    // every URL — is no list, not a crash.
    threadList({ nope: true });
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    const list = await screen.findByRole("navigation", { name: "Threads" });
    expect(within(list).queryAllByRole("button")).toHaveLength(0);
  });
});
