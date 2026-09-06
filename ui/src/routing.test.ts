import { describe, it, expect } from "vitest";

import { pathForRoute, routeFromPath } from "./routing";

/** The shape the server mints: 16 random bytes as 22 URL-safe characters. */
const address = "v76BBy2b1nMYOFl2Lnm9JQ";

describe("routeFromPath", () => {
  it("reads a thread out of its address", () => {
    expect(routeFromPath(`/thread/${address}`)).toEqual({ view: "thread", id: address });
  });

  it("reads the two pages and the share link", () => {
    expect(routeFromPath("/repos")).toEqual({ view: "repos" });
    expect(routeFromPath("/shared")).toEqual({ view: "shared" });
    expect(routeFromPath("/share/kd8Qw1rZ")).toEqual({ view: "share", token: "kd8Qw1rZ" });
  });

  it("falls back to the unasked question", () => {
    // Including the addresses that look like a thread but are not. Anything
    // that is not the exact shape lands here rather than becoming a fetch of a
    // thread that cannot exist: a 404 that Ask reads as "not right now" rather
    // than as a dead thread, leaving a blank column and that URL in the bar.
    //
    // "/thread/19" is on this list on purpose. Threads were addressed by row
    // number for one release and those URLs are not redirected — a redirect
    // would keep the counter reachable for good. The last three are the
    // boundaries of the shape itself: one character too many, one too few, and
    // one outside the alphabet.
    for (const path of [
      "/", "/new", "/thread/", "/thread/abc", "/thread/19",
      `/thread/${address}x`, `/thread/${address.slice(1)}`,
      "/thread/v76BBy2b1nMYOFl2Lnm9J.", "/share/", "/nope",
    ]) {
      expect(routeFromPath(path)).toEqual({ view: "new" });
    }
  });

  it("round-trips every route", () => {
    for (const route of [
      { view: "new" },
      { view: "thread", id: address },
      { view: "repos" },
      { view: "shared" },
      { view: "share", token: "kd8Qw1rZ" },
    ] as const) {
      expect(routeFromPath(pathForRoute(route))).toEqual(route);
    }
  });
});
