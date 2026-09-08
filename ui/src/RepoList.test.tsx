import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import RepoList, { byProject, unconnected, wiringSpec } from "./RepoList";

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
  project: "peeq",
  kind: "",
  description: "",
  uses: [] as string[],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("RepoList", () => {
  it("shows the counts and a shortened HEAD SHA", async () => {
    respondWith(200, [peeq]);

    render(<RepoList />);

    // Named twice on purpose: once as the project heading, once as the
    // repository row. A project of one is still a project.
    await screen.findByRole("heading", { name: "peeq" });
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

  // Seven columns do not fit a phone. Without its own scroller the table
  // overflowed the page's, dragging the whole Repos page sideways.
  it("scrolls the table inside its own box, name column pinned", async () => {
    respondWith(200, [peeq]);

    render(<RepoList />);
    await screen.findByRole("heading", { name: "peeq" });

    const table = screen.getByRole("table");
    expect(table.className).toContain("min-w-[720px]");
    expect(table.parentElement!.className).toContain("overflow-x-auto");
    // The pinned column carries the error stripe, so an error stays in sight
    // however far the row is scrolled.
    const name = within(screen.getByRole("table")).getByText("peeq").closest("td")!;
    expect(name.className).toContain("sticky");
    expect(name.className).toContain("left-0");
    expect(name.className).toContain("bg-panel");
  });

  it("stacks the stats two-up on a phone", async () => {
    respondWith(200, [peeq]);

    render(<RepoList />);
    // label div -> the Stat -> the block holding all five.
    const stats = (await screen.findByText("Repositories")).parentElement!.parentElement!;
    expect(stats.className).toContain("grid-cols-2");
    expect(stats.className).toContain("sm:flex");
  });
});

/** shop is one product in four repositories: a storefront UI calling one of two
 * backends, and a queue consumer nothing in the project reaches. */
const shop = [
  { ...peeq, name: "shop-ui", project: "shop", kind: "ui", description: "Storefront, React.", uses: ["shop-backend"] },
  { ...peeq, name: "shop-backend", project: "shop", kind: "backend", description: "Checkout." },
  { ...peeq, name: "shop-admin-backend", project: "shop", kind: "backend", description: "Admin API." },
  { ...peeq, name: "shop-events", project: "shop", kind: "consumer", description: "Kafka consumer." },
];

describe("project grouping", () => {
  it("groups by project whatever order the rows arrive in", () => {
    const got = byProject([shop[3], { ...peeq, name: "legacy-crm", project: "legacy-crm" }, shop[0]]);

    expect(got.map((p) => p.name)).toEqual(["legacy-crm", "shop"]);
    expect(got[1].repos.map((r) => r.name)).toEqual(["shop-events", "shop-ui"]);
  });

  it("falls back to the repository name for a row written before projects", () => {
    // repos.Load refuses an entry without a project, so this can only be an old
    // row. Grouping those under "" would make one nameless product of them all.
    const got = byProject([{ ...peeq, name: "old", project: "" }]);

    expect(got.map((p) => p.name)).toEqual(["old"]);
  });
});

describe("wiringSpec", () => {
  it("draws the declared edges, entry points as pills", () => {
    const spec = wiringSpec({ name: "shop", repos: shop })!;

    expect(spec.edges).toEqual([{ from: "shop-ui", to: "shop-backend" }]);
    // Nothing uses shop-ui, so it is where the picture starts.
    expect(spec.nodes.find((n) => n.id === "shop-ui")!.kind).toBe("start");
    expect(spec.nodes.find((n) => n.id === "shop-backend")!.kind).toBe("step");
    // Only what an edge touches. A member nothing connects to would be a box
    // floating beside the graph and named in the line beneath it — the same
    // fact twice, once as a picture that says nothing.
    expect(spec.nodes.map((n) => n.id).sort()).toEqual(["shop-backend", "shop-ui"]);
    // Configuration is not code, so no node cites anything. AGENTS.md: a node
    // cites code or nothing, and is still drawn with no sources.
    expect(spec.nodes.every((n) => n.src.length === 0)).toBe(true);
  });

  it("draws nothing for a project with no declared edge", () => {
    // A project of one has no wiring, and a picture of unconnected boxes says
    // less than the table under it.
    expect(wiringSpec({ name: "peeq", repos: [peeq] })).toBeNull();
    expect(wiringSpec({ name: "shop", repos: [shop[1], shop[3]] })).toBeNull();
  });

  it("ignores an edge pointing out of the project", () => {
    // repos.Load already refuses one, so this can only be a hand-written row —
    // and an arrow leaving the picture is worse than no arrow.
    const spec = wiringSpec({
      name: "shop",
      repos: [{ ...shop[0], uses: ["something-else"] }, shop[1]],
    });

    expect(spec).toBeNull();
  });

  it("names the members no declared edge reaches", () => {
    // The disambiguating half: two repositories say "backend", and this is what
    // keeps an answer about the storefront away from the admin API.
    expect(unconnected({ name: "shop", repos: shop }).map((r) => r.name)).toEqual([
      "shop-admin-backend",
      "shop-events",
    ]);
  });

  it("calls nothing unconnected when there are no edges at all", () => {
    // "Nothing uses any of them" is noise when there is no arrow to contrast
    // with, and the useful fact — one product — is already in the heading.
    expect(unconnected({ name: "shop", repos: [shop[1], shop[3]] })).toEqual([]);
  });
});

describe("the Projects page", () => {
  it("draws one panel per project, with the wiring inside it", async () => {
    respondWith(200, shop);

    render(<RepoList />);

    await screen.findByRole("heading", { name: "shop" });
    expect(screen.getByRole("img", { name: /flow diagram/i })).toBeTruthy();
    expect(screen.getByText(/No declared connection/)).toBeTruthy();
    // One table, four rows: the project is one panel, not four.
    expect(screen.getAllByRole("table")).toHaveLength(1);
  });

  it("gives a project of one a heading and a row and no picture", async () => {
    respondWith(200, [peeq]);

    render(<RepoList />);

    await screen.findByRole("heading", { name: "peeq" });
    expect(screen.queryByRole("img", { name: /flow diagram/i })).toBeNull();
  });
});
