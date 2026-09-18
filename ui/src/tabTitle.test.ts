import { describe, it, expect } from "vitest";

import { tabTitle } from "./tabTitle";

describe("tabTitle", () => {
  it("names the page the way its heading does", () => {
    expect(tabTitle({ view: "new" }, null)).toBe("New question · Rongo");
    expect(tabTitle({ view: "threads" }, null)).toBe("Threads · Rongo");
    expect(tabTitle({ view: "projects" }, null)).toBe("Projects · Rongo");
    expect(tabTitle({ view: "shared" }, null)).toBe("Shared · Rongo");
    expect(tabTitle({ view: "memory" }, null)).toBe("Memory · Rongo");
  });

  it("names an open thread by its settled title", () => {
    expect(tabTitle({ view: "thread", id: "v76BBy2b1nMYOFl2Lnm9JQ" }, "Wie wird ein Fall eröffnet")).toBe(
      "Wie wird ein Fall eröffnet · Rongo",
    );
  });

  it("stays plain until the thread has a title", () => {
    // Pending title, thread not yet fetched, or a dead address: the tab does
    // not say "New question" about a thread, and never shows the placeholder.
    expect(tabTitle({ view: "thread", id: "v76BBy2b1nMYOFl2Lnm9JQ" }, null)).toBe("Rongo");
  });

  it("leaves the share page to SharePage", () => {
    expect(tabTitle({ view: "share", token: "kd8Qw1rZ" }, null)).toBe("Rongo");
  });
});
