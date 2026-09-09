import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import RepoList, { byProject, parkedSummary, type Repo } from "./RepoList";

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

const base: Repo = {
  name: "shop-ui",
  branch: "main",
  last_sha: "611255ac0ffee11",
  last_run_at: "2026-08-17T09:30:00Z",
  files: 100,
  chunks: 500,
  modules: 4,
  enabled: true,
  snapshot: false,
  last_error: "",
  project: "shop",
  part: "ui",
  description: "",
  uses: [],
};

const repo = (over: Partial<Repo>): Repo => ({ ...base, ...over });

/** The whole sentence, as a reader sees it. */
const line = (repos: Repo[]) => {
  const p = parkedSummary(repos);
  return p && `${p.count} ${p.note}`;
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("parkedSummary", () => {
  it("says nothing at all when nothing is parked", () => {
    // The ordinary case, and the one that has to stay completely quiet: null,
    // not an empty string, so the component draws no paragraph.
    expect(parkedSummary([repo({}), repo({ name: "shop-backend" })])).toBeNull();
  });

  it("counts one parked repository in the singular", () => {
    expect(line([repo({}), repo({ name: "shop-events", enabled: false })])).toBe(
      "1 repository is disabled and not shown. It keeps its index and is not polled.",
    );
  });

  it("counts several across several projects", () => {
    expect(
      line([
        repo({}),
        repo({ name: "shop-events", enabled: false }),
        repo({ name: "crm-api", project: "crm", enabled: false }),
        repo({ name: "crm-jobs", project: "crm" }),
        repo({ name: "crm-ui", project: "crm", enabled: false }),
      ]),
    ).toBe(
      "3 repositories in 2 projects are disabled and not shown. They keep their index and are not polled.",
    );
  });

  it("says so when the one project it names went entirely", () => {
    // A wholly parked project loses its panel AND its name, so nothing else on
    // the page would hint the product exists.
    expect(
      line([
        repo({}),
        repo({ name: "crm-api", project: "crm", enabled: false }),
        repo({ name: "crm-ui", project: "crm", enabled: false }),
      ]),
    ).toBe(
      "2 repositories in 1 project are disabled and not shown, the project entirely. They keep their index and are not polled.",
    );
  });

  it("says so when one of several projects went entirely", () => {
    expect(
      line([
        repo({}),
        repo({ name: "shop-events", enabled: false }),
        repo({ name: "crm-api", project: "crm", enabled: false }),
        repo({ name: "crm-ui", project: "crm", enabled: false }),
      ]),
    ).toBe(
      "3 repositories in 2 projects are disabled and not shown, one project entirely. They keep their index and are not polled.",
    );
  });

  it("treats a repository with no project as a project of its own", () => {
    // The same fallback the page groups by, so the two counts cannot disagree.
    expect(line([repo({}), repo({ name: "loner", project: "", enabled: false })])).toBe(
      "1 repository is disabled and not shown, the project entirely. It keeps its index and is not polled.",
    );
  });
});

describe("RepoList with parked repositories", () => {
  it("draws neither the row nor the panel of a parked repository", async () => {
    respondWith(200, [
      repo({}),
      repo({ name: "crm-api", project: "crm", enabled: false }),
      repo({ name: "crm-ui", project: "crm", enabled: false }),
    ]);

    render(<RepoList />);

    await screen.findByRole("heading", { name: "shop" });
    expect(screen.queryByRole("heading", { name: "crm" })).toBeNull();
    expect(screen.queryByText("crm-api")).toBeNull();
    expect(screen.queryByText("crm-ui")).toBeNull();
  });

  it("keeps the totals agreeing with the panels underneath them", async () => {
    // The stat block describes what is drawn below it. Counting hidden rows
    // would leave the numbers arguing with the page.
    respondWith(200, [
      repo({ files: 100, chunks: 500, modules: 4 }),
      repo({ name: "shop-events", enabled: false, files: 999, chunks: 999, modules: 9 }),
    ]);

    render(<RepoList />);

    await screen.findByRole("heading", { name: "shop" });
    // 100 shows twice: the Files stat and the one drawn row. The parked
    // repository's 999 must be in neither, so the sum is never 1099.
    expect(screen.getAllByText("100")).toHaveLength(2);
    expect(screen.queryByText("1099")).toBeNull();
    expect(screen.queryByText("999")).toBeNull();
    expect(screen.getByText(/1 repository is disabled and not shown/)).toBeTruthy();
  });

  it("says all of it is switched off rather than showing six zeros", async () => {
    // "Nothing is configured" and "all of it is parked" are different facts
    // with different fixes.
    respondWith(200, [repo({ enabled: false }), repo({ name: "shop-backend", enabled: false })]);

    render(<RepoList />);

    expect(
      await screen.findByText(/2 repositories in 1 project are disabled and not shown/),
    ).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("groups only what it was given, so the page and the line cannot disagree", () => {
    // byProject is fed the filtered list, which is what drops a wholly parked
    // project's panel; this pins that it is the caller's filter doing it.
    const all = [repo({}), repo({ name: "crm-api", project: "crm", enabled: false })];
    expect(byProject(all.filter((r) => r.enabled)).map((p) => p.name)).toEqual(["shop"]);
  });
});
