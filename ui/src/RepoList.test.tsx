import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import RepoList from "./RepoList";

/** Every test drives the component through fetch. Nothing here reaches a network. */
function respondWith(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: status >= 200 && status < 300,
      status,
      json: async () => body,
      text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
    })),
  );
}

const peeq = {
  name: "peeq",
  branch: "master",
  last_sha: "611255ac0ffee11",
  last_run_at: "2026-08-17T09:30:00Z",
  files: 412,
  chunks: 3120,
  modules: 34,
  enabled: true,
  last_error: "",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("RepoList", () => {
  it("shows the counts and a shortened HEAD SHA", async () => {
    respondWith(200, [peeq]);

    render(<RepoList />);

    await screen.findByText("peeq");
    // Scoped to the table: the strip above it sums the same numbers.
    const table = within(screen.getByRole("table"));
    expect(table.getByText("412")).toBeTruthy();
    expect(table.getByText("3120")).toBeTruthy();
    expect(table.getByText("34")).toBeTruthy();
    // The full SHA is unreadable in a table and the short form is what a
    // person compares against the forge.
    expect(table.getByText("611255a")).toBeTruthy();
  });

  it("shows a vanished branch as a loud error", async () => {
    // A silent stop leaves the index frozen at months-old code while the page
    // looks healthy — the one failure this page exists to make visible.
    respondWith(200, [
      { ...peeq, name: "shop-backend", branch: "release-2024.3", last_error: "branch release-2024.3 is gone upstream" },
    ]);

    render(<RepoList />);

    const row = await screen.findByRole("row", { name: /shop-backend/ });
    expect(row.textContent).toContain("branch release-2024.3 is gone upstream");
    expect(row.getAttribute("data-state")).toBe("error");
  });

  it("marks a disabled repository instead of hiding it", async () => {
    respondWith(200, [peeq, { ...peeq, name: "legacy-crm", enabled: false }]);

    render(<RepoList />);

    const row = await screen.findByRole("row", { name: /legacy-crm/ });
    expect(row.getAttribute("data-state")).toBe("disabled");
    expect(row.textContent).toContain("Disabled");
  });

  it("tells 'nothing indexed yet' apart from an error", async () => {
    respondWith(200, []);

    render(<RepoList />);

    await screen.findByText(/No repositories/);
  });

  it("says so when the status cannot be fetched", async () => {
    // Not an empty table: "nothing is configured" and "the server cannot tell
    // you" must never look the same.
    respondWith(503, "repository status unavailable");

    render(<RepoList />);

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toMatch(/cannot be fetched/i);
    });
    expect(screen.queryByText(/No repositories/)).toBeNull();
  });
});
