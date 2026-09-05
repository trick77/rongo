import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import SharedLinks from "./SharedLinks";
import type { Share } from "./api";

afterEach(() => vi.unstubAllGlobals());

const live: Share = {
  token: "kd8Qw1rZ7mB3xN2pLcTvAg",
  path: "/share/kd8Qw1rZ7mB3xN2pLcTvAg",
  thread_id: 7,
  title: "How does routing decide?",
  up_to_message_id: 12,
  turns: 2,
  newer: 0,
  shared_at: "2026-09-04T14:09:00Z",
  updated_at: "2026-09-04T14:09:00Z",
};

function listing(shares: Share[]) {
  const mock = vi.fn(async () => ({ ok: true, status: 200, json: async () => shares }));
  vi.stubGlobal("fetch", mock);
  return mock;
}

describe("SharedLinks", () => {
  it("says where Share lives when nothing is shared", async () => {
    listing([]);
    render(<SharedLinks onOpenThread={() => {}} />);

    expect(await screen.findByText(/No thread is shared/)).toBeTruthy();
  });

  it("lists a live link with the turns it covers", async () => {
    listing([live]);
    render(<SharedLinks onOpenThread={() => {}} />);

    await screen.findByText("How does routing decide?");
    expect(screen.getByText(live.path)).toBeTruthy();
    expect(screen.getByText("Live")).toBeTruthy();
  });

  it("marks a link the thread has moved on from", async () => {
    listing([{ ...live, newer: 3 }]);
    render(<SharedLinks onOpenThread={() => {}} />);

    // A badge, not a call to action: raising the ceiling belongs in the
    // thread's own dialog, where the turns that would be added are visible.
    expect(await screen.findByText("3 turns newer")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Update/ })).toBeNull();
  });

  it("copies the absolute link", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    listing([live]);
    render(<SharedLinks onOpenThread={() => {}} />);
    await screen.findByText("How does routing decide?");

    fireEvent.click(screen.getByRole("button", { name: /Copy link/ }));

    await screen.findByRole("button", { name: /Copied/ });
    expect(writeText).toHaveBeenCalledWith(window.location.origin + live.path);
  });

  it("takes a link off the list when it is revoked", async () => {
    const mock = vi.fn(async (_url: string, init?: { method?: string }) =>
      init?.method === "DELETE"
        ? { ok: true, status: 204, json: async () => ({}) }
        : { ok: true, status: 200, json: async () => [live] },
    );
    vi.stubGlobal("fetch", mock);
    const onChange = vi.fn();
    render(<SharedLinks onChange={onChange} onOpenThread={() => {}} />);
    await screen.findByText("How does routing decide?");

    fireEvent.click(screen.getByRole("button", { name: /Revoke/ }));

    // A revoked link is not a link; the audit view is not a history.
    await waitFor(() => expect(screen.queryByText("How does routing decide?")).toBeNull());
    expect(onChange).toHaveBeenCalled();
  });

  it("opens the thread a row names", async () => {
    listing([live]);
    const onOpenThread = vi.fn();
    render(<SharedLinks onOpenThread={onOpenThread} />);

    fireEvent.click(await screen.findByRole("button", { name: "How does routing decide?" }));

    expect(onOpenThread).toHaveBeenCalledWith(7);
  });
});
