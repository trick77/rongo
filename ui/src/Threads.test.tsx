import { describe, it, expect, vi, afterEach, type Mock } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Threads from "./Threads";

function threadList(list: unknown, ok = true) {
  const fetchMock = vi.fn(async () => ({ ok, status: ok ? 200 : 500, json: async () => list }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

const two = [
  { id: 7, title: "How does shipping work?", created_at: "2026-08-17T10:00:00Z" },
  { id: 3, title: "Where does the token come from?", created_at: "2026-08-16T10:00:00Z" },
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
    render(<Threads activeId={3} onSelect={() => {}} version={0} />);
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
    expect(onSelect).toHaveBeenCalledWith(3);
  });

  // The model-written title replaces the placeholder in a background goroutine
  // with no way to push it. Without a reload the sidebar shows the truncated
  // question until someone reloads the page.
  it("reloads when the version changes", async () => {
    const fetchMock = threadList(two);
    const { rerender } = render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    await screen.findByText("How does shipping work?");
    rerender(<Threads activeId={null} onSelect={() => {}} version={1} />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
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
    render(<Threads activeId={7} onSelect={onSelect} version={0} busy busyId={7} />);
    const other = await screen.findByRole("button", { name: "Where does the token come from?" });
    expect((other as HTMLButtonElement).disabled).toBe(false);
    const user = userEvent.setup();
    await user.click(other);
    expect(onSelect).toHaveBeenCalledWith(3);
  });

  // With the page nav gone, this row is the only way back to a streaming
  // answer from the Repos page.
  it("keeps the running thread's own row clickable while it streams", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId={7} onSelect={onSelect} version={0} busy busyId={7} />);
    const own = await screen.findByRole("button", { name: "How does shipping work?" });
    expect((own as HTMLButtonElement).disabled).toBe(false);
    const user = userEvent.setup();
    await user.click(own);
    expect(onSelect).toHaveBeenCalledWith(7);
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
      { id: 9, title: "Asked this morning", created_at: days(0) },
      { id: 4, title: "Asked earlier in the week", created_at: days(3) },
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

  describe("the row menu", () => {
    /** Opens the menu on one row and hands back the user-event session. */
    async function openMenu(title: string) {
      const user = userEvent.setup();
      await user.click(await screen.findByRole("button", { name: "Actions for " + title }));
      return user;
    }

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

      expect(onSelect).toHaveBeenCalledWith(3);
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

      rerender(<Threads activeId={7} onSelect={() => {}} version={0} busy busyId={7} />);

      expect(screen.queryByRole("menu")).toBeNull();
    });

    // Deleting the thread being written would pull the record out from under
    // the answer still landing on it. Only that one: every other row keeps
    // its actions, because nothing is being written into it.
    it("is withheld from the thread being written, and from no other", async () => {
      threadList(two);
      render(<Threads activeId={7} onSelect={() => {}} version={0} busy busyId={7} />);
      await screen.findByRole("button", { name: "How does shipping work?" });
      expect(screen.queryByRole("button", { name: "Actions for How does shipping work?" })).toBeNull();
      expect(screen.getByRole("button", { name: "Actions for Where does the token come from?" })).toBeTruthy();
    });

    // The reader has walked away from the answer and is reading another
    // thread: the row they are ON keeps its actions, and the one being
    // written — wherever it sits — does not.
    it("follows the thread being written, not the one on screen", async () => {
      threadList(two);
      render(<Threads activeId={3} onSelect={() => {}} version={0} busy busyId={7} />);
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
      expect(fetch).toHaveBeenCalledTimes(1); // the list load, and nothing more

      (fetch as unknown as Mock).mockResolvedValueOnce({ ok: true, status: 204 });
      await user.click(screen.getByRole("button", { name: "Delete" }));

      expect(fetch).toHaveBeenCalledWith("/api/threads/7", { method: "DELETE" });
      expect(onDeleted).toHaveBeenCalledWith(7);
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
