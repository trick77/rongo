import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Threads from "./Threads";

function threadList(list: unknown, ok = true) {
  const fetchMock = vi.fn(async () => ({ ok, status: ok ? 200 : 500, json: async () => list }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

const two = [
  { id: 7, title: "Wie laeuft der Versand?", created_at: "2026-08-17T10:00:00Z" },
  { id: 3, title: "Woher kommt der Token?", created_at: "2026-08-16T10:00:00Z" },
];

describe("Threads", () => {
  it("laedt die Liste und zeigt die Titel", async () => {
    threadList(two);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    expect(await screen.findByText("Wie laeuft der Versand?")).toBeTruthy();
    expect(screen.getByText("Woher kommt der Token?")).toBeTruthy();
  });

  it("markiert den offenen Thread", async () => {
    threadList(two);
    render(<Threads activeId={3} onSelect={() => {}} version={0} />);
    const active = await screen.findByRole("button", { name: "Woher kommt der Token?" });
    expect(active.getAttribute("aria-current")).toBe("true");
    expect(
      screen.getByRole("button", { name: "Wie laeuft der Versand?" }).getAttribute("aria-current"),
    ).toBeNull();
  });

  it("gibt die Wahl nach oben weiter", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId={null} onSelect={onSelect} version={0} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Woher kommt der Token?" }));
    expect(onSelect).toHaveBeenCalledWith(3);
  });

  it("startet mit «Neue Frage» einen leeren Thread", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId={7} onSelect={onSelect} version={0} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Neue Frage" }));
    expect(onSelect).toHaveBeenCalledWith(null);
  });

  // The model-written title replaces the placeholder in a background goroutine
  // with no way to push it. Without a reload the sidebar shows the truncated
  // question until someone reloads the page.
  it("laedt neu, wenn die Version sich aendert", async () => {
    const fetchMock = threadList(two);
    const { rerender } = render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    await screen.findByText("Wie laeuft der Versand?");
    rerender(<Threads activeId={null} onSelect={() => {}} version={1} />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });

  it("bleibt bedienbar, wenn der Server die Liste verweigert", async () => {
    threadList(null, false);
    render(<Threads activeId={null} onSelect={() => {}} version={0} />);
    expect(await screen.findByRole("button", { name: "Neue Frage" })).toBeTruthy();
  });

  it("sperrt den Wechsel, solange eine Antwort laeuft", async () => {
    threadList(two);
    const onSelect = vi.fn();
    render(<Threads activeId={7} onSelect={onSelect} version={0} busy />);
    const other = await screen.findByRole("button", { name: "Woher kommt der Token?" });
    expect((other as HTMLButtonElement).disabled).toBe(true);
    const user = userEvent.setup();
    await user.click(other);
    expect(onSelect).not.toHaveBeenCalled();
  });
});
