import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("mermaid", () => ({
  default: {
    initialize: vi.fn(),
    parse: async () => undefined,
    render: async (id: string) => ({ svg: `<svg id="${id}"><g class="drawn"></g></svg>` }),
  },
}));

import RepoList, { libraryNames, unconnected, usedBy, wiringSpec, type Repo } from "./RepoList";

const base: Repo = {
  name: "",
  branch: "main",
  last_sha: "611255ac0ffee11",
  last_run_at: "2026-09-17T09:30:00Z",
  last_indexed_at: "2026-09-15T06:00:00Z",
  files: 10,
  chunks: 100,
  modules: 3,
  enabled: true,
  snapshot: false,
  last_error: "",
  project: "",
  part: "",
  description: "",
  uses: [],
};

/** A library two products are built on: shop's backend and billing's api both
 * name it; shop's ui names nothing. */
const commons: Repo = { ...base, name: "acme-commons", project: "acme-commons", library: true };
const shopBackend: Repo = { ...base, name: "shop-backend", project: "shop", part: "backend", uses: ["acme-commons"] };
const shopUi: Repo = { ...base, name: "shop-ui", project: "shop", part: "ui", uses: ["shop-backend"] };
const billingApi: Repo = { ...base, name: "billing-api", project: "billing", uses: ["acme-commons"] };
const all = [commons, shopBackend, shopUi, billingApi];

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("a shared library", () => {
  it("is drawn into the wiring of a project using it, as a terminal marked library", () => {
    const spec = wiringSpec({ name: "shop", repos: [shopBackend, shopUi] }, libraryNames(all))!;

    expect(spec.edges).toEqual([
      { from: "shop-backend", to: "acme-commons" },
      { from: "shop-ui", to: "shop-backend" },
    ]);
    const lib = spec.nodes.find((n) => n.id === "acme-commons")!;
    expect(lib.kind).toBe("end");
    expect(lib.label).toBe("acme-commons · library");
  });

  it("makes a project of one with only a library edge draw its wiring", () => {
    // Without the library the picture would be null; the edge is the one
    // thing this project declares.
    const spec = wiringSpec({ name: "billing", repos: [billingApi] }, libraryNames(all))!;
    expect(spec.nodes.map((n) => n.id).sort()).toEqual(["acme-commons", "billing-api"]);
  });

  it("is still ignored as a target when it is not on the page", () => {
    // A parked library is filtered out before the grouping, and then the
    // edge is a hand-written stray again: nothing to draw.
    expect(wiringSpec({ name: "billing", repos: [billingApi] })).toBeNull();
  });

  it("counts a member as connected when its only edge is to a library", () => {
    expect(unconnected({ name: "shop", repos: [shopBackend, shopUi] }, libraryNames(all))).toEqual([]);
    expect(unconnected({ name: "billing", repos: [billingApi] }, libraryNames(all))).toEqual([]);
  });

  it("knows which repositories are built on it", () => {
    expect(usedBy("acme-commons", all)).toEqual(["billing-api", "shop-backend"]);
    expect(usedBy("shop-backend", all)).toEqual(["shop-ui"]);
  });

  it("gets its own panel after the projects, saying who uses it, and its own count", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, status: 200, json: async () => all, text: async () => "" })),
    );

    render(<RepoList />);

    await screen.findByRole("heading", { name: "acme-commons" });
    const headings = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(headings).toEqual(["billing", "shop", "acme-commons"]);
    expect(screen.getByText(/used by/).textContent).toContain("billing-api, shop-backend");
    // Two products, one library: the Projects count says products.
    expect(screen.getByText("Projects").parentElement?.textContent).toContain("2");
    expect(screen.getByText("Libraries").parentElement?.textContent).toContain("1");
  });
});
