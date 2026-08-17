import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
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
  it("zeigt Zaehlungen und einen gekuerzten HEAD-SHA", async () => {
    respondWith(200, [peeq]);

    render(<RepoList />);

    await screen.findByText("peeq");
    expect(screen.getByText("412")).toBeTruthy();
    expect(screen.getByText("3120")).toBeTruthy();
    expect(screen.getByText("34")).toBeTruthy();
    // The full SHA is unreadable in a table and the short form is what a
    // person compares against the forge.
    expect(screen.getByText("611255a")).toBeTruthy();
  });

  it("zeigt einen verschwundenen Branch als lauten Fehler", async () => {
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

  it("markiert ein stillgelegtes Repository, statt es zu verbergen", async () => {
    respondWith(200, [peeq, { ...peeq, name: "legacy-crm", enabled: false }]);

    render(<RepoList />);

    const row = await screen.findByRole("row", { name: /legacy-crm/ });
    expect(row.getAttribute("data-state")).toBe("disabled");
    expect(row.textContent).toContain("Stillgelegt");
  });

  it("unterscheidet 'noch nichts indexiert' von einem Fehler", async () => {
    respondWith(200, []);

    render(<RepoList />);

    await screen.findByText(/Noch keine Repositories/);
  });

  it("sagt es, wenn der Status nicht abrufbar ist", async () => {
    // Not an empty table: "nothing is configured" and "the server cannot tell
    // you" must never look the same.
    respondWith(503, "repository status unavailable");

    render(<RepoList />);

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toMatch(/nicht abrufbar/i);
    });
    expect(screen.queryByText(/Noch keine Repositories/)).toBeNull();
  });
});
