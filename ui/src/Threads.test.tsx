import { describe, it, expect, vi, afterEach } from "vitest";
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

  it("locks switching while an answer is streaming", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId={7} onSelect={onSelect} version={0} busy />);
    const other = await screen.findByRole("button", { name: "Where does the token come from?" });
    expect((other as HTMLButtonElement).disabled).toBe(true);
    const user = userEvent.setup();
    await user.click(other);
    expect(onSelect).not.toHaveBeenCalled();
  });

  // With the page nav gone, this row is the only way back to a streaming
  // answer from the Repos page. Locking it would strand the reader there
  // until the turn finished.
  it("keeps the running thread's own row clickable while it streams", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId={7} onSelect={onSelect} version={0} busy />);
    const own = await screen.findByRole("button", { name: "How does shipping work?" });
    expect((own as HTMLButtonElement).disabled).toBe(false);
    const user = userEvent.setup();
    await user.click(own);
    expect(onSelect).toHaveBeenCalledWith(7);
  });
});
