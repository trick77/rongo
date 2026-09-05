import { describe, it, expect } from "vitest";

import { pathForRoute, routeFromPath } from "./routing";

describe("routeFromPath", () => {
  it("reads a thread out of its address", () => {
    expect(routeFromPath("/thread/7")).toEqual({ view: "thread", id: 7 });
  });

  it("reads the two pages and the share link", () => {
    expect(routeFromPath("/repos")).toEqual({ view: "repos" });
    expect(routeFromPath("/shared")).toEqual({ view: "shared" });
    expect(routeFromPath("/share/kd8Qw1rZ")).toEqual({ view: "share", token: "kd8Qw1rZ" });
  });

  it("falls back to the unasked question", () => {
    // Including the addresses that look like a thread but are not: NaN carried
    // into a fetch of /api/threads/NaN is a request nobody can answer.
    for (const path of ["/", "/new", "/thread/", "/thread/abc", "/thread/0", "/thread/-2", "/share/", "/nope"]) {
      expect(routeFromPath(path)).toEqual({ view: "new" });
    }
  });

  it("round-trips every route", () => {
    for (const route of [
      { view: "new" },
      { view: "thread", id: 12 },
      { view: "repos" },
      { view: "shared" },
      { view: "share", token: "kd8Qw1rZ" },
    ] as const) {
      expect(routeFromPath(pathForRoute(route))).toEqual(route);
    }
  });
});
