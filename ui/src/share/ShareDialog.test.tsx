import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import ShareDialog from "./ShareDialog";
import type { Share } from "./api";

afterEach(() => vi.unstubAllGlobals());

const live: Share = {
  token: "kd8Qw1rZ7mB3xN2pLcTvAg",
  path: "/share/kd8Qw1rZ7mB3xN2pLcTvAg",
  thread_id: 7,
  title: "How does shipping work?",
  up_to_message_id: 12,
  turns: 2,
  newer: 0,
  shared_at: "2026-09-04T14:09:00Z",
  updated_at: "2026-09-04T14:09:00Z",
};

function clipboard() {
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  return writeText;
}

function open(share: Share | null, onChange = vi.fn()) {
  render(
    <ShareDialog
      threadID={7}
      title="How does shipping work?"
      share={share}
      onCancel={() => {}}
      onChange={onChange}
    />,
  );
  return onChange;
}

describe("ShareDialog", () => {
  it("offers to make a link when the thread has none", () => {
    open(null);
    expect(screen.getByRole("heading", { name: "Share thread" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Create link" })).toBeTruthy();
    // Nothing to copy and nothing to take back yet.
    expect(screen.queryByRole("button", { name: "Copy link" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Revoke link" })).toBeNull();
  });

  it("makes the link and shows it", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => live })));
    const onChange = open(null);
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Create link" }));

    await screen.findByText(new RegExp(live.token));
    expect(onChange).toHaveBeenCalledWith(live);
  });

  // fireEvent rather than userEvent: userEvent.setup() installs a clipboard
  // stub of its own and would shadow these, as Ask's own copy test found.
  it("copies the absolute link and says so for a moment", async () => {
    const writeText = clipboard();
    open(live);

    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));

    // The server sends a path; the origin is the browser's own, because a
    // rongo behind a TLS proxy cannot know it.
    await screen.findByRole("button", { name: "Copied" });
    expect(writeText).toHaveBeenCalledWith(window.location.origin + live.path);
  });

  it("says so when the clipboard refuses, and leaves the link on screen", async () => {
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn(async () => Promise.reject(new Error("denied"))) },
      configurable: true,
    });
    open(live);

    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));

    await screen.findByRole("alert");
    // There is no execCommand fallback, so the URL itself has to stay
    // selectable — that is the way out.
    expect(screen.getByText(new RegExp(live.token))).toBeTruthy();
  });

  it("offers to update when the thread has moved on, and keeps the same link", async () => {
    const raised = { ...live, newer: 0, turns: 5 };
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => raised })));
    open({ ...live, newer: 3 });
    const user = userEvent.setup();
    expect(screen.getByText(/3 questions have been asked since/)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Update link" }));

    await waitFor(() => expect(screen.queryByText(/questions have been asked since/)).toBeNull());
    expect(screen.getByText(new RegExp(live.token))).toBeTruthy();
  });

  it("takes the link back without asking twice", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 204 })));
    const onChange = open(live);
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Revoke link" }));

    // Undoable — sharing again returns the same link — so a confirmation
    // would be theatre.
    await screen.findByRole("button", { name: "Create link" });
    expect(onChange).toHaveBeenCalledWith(null);
  });

  it("explains a 409 rather than showing a bare failure", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 409 })));
    open(null);
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Create link" }));

    expect((await screen.findByRole("alert")).textContent).toMatch(/still being written/);
  });
});
