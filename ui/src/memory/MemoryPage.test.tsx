import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import MemoryPage from "./MemoryPage";
import type { Memory } from "./api";

afterEach(() => vi.unstubAllGlobals());

const rule: Memory = {
  id: 7,
  text: "Never draw flowchart diagrams.",
  scope_live: true,
  thread_id: "v76BBy2b1nMYOFl2Lnm9JQ",
  created_at: "2026-09-18T10:00:00Z",
};

function listing(enabled: boolean, memories: Memory[]) {
  const mock = vi.fn(async (_url: string, opts?: RequestInit) => {
    if (opts?.method === "DELETE") return { ok: true, status: 204, json: async () => ({}) };
    return { ok: true, status: 200, json: async () => ({ enabled, memories }) };
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

describe("MemoryPage", () => {
  it("says how a rule is given when nothing is kept", async () => {
    listing(true, []);
    render(<MemoryPage onOpenThread={() => {}} />);
    expect(await screen.findByText(/Nothing is kept yet/)).toBeTruthy();
  });

  it("says memory is off for the deployment", async () => {
    listing(false, []);
    const onCount = vi.fn();
    render(<MemoryPage onOpenThread={() => {}} onCount={onCount} />);
    expect(await screen.findByText(/Memory is off for this deployment/)).toBeTruthy();
    // No count on a page that keeps nothing.
    expect(onCount).not.toHaveBeenCalledWith(0);
  });

  it("lists the rules with where they hold and where they came from", async () => {
    listing(true, [rule, { ...rule, id: 8, text: "Skip the tests.", scope: "shop", thread_id: undefined }]);
    const onCount = vi.fn();
    const onOpenThread = vi.fn();
    render(<MemoryPage onOpenThread={onOpenThread} onCount={onCount} />);

    await screen.findByText("Never draw flowchart diagrams.");
    expect(screen.getByText("everywhere")).toBeTruthy();
    expect(screen.getByText("shop")).toBeTruthy();
    await waitFor(() => expect(onCount).toHaveBeenCalledWith(2));

    // Only the rule whose turn still exists links back to it.
    const links = screen.getAllByRole("button", { name: "from thread" });
    expect(links).toHaveLength(1);
    fireEvent.click(links[0]);
    expect(onOpenThread).toHaveBeenCalledWith("v76BBy2b1nMYOFl2Lnm9JQ");
  });

  it("marks a scope the index no longer carries", async () => {
    listing(true, [{ ...rule, scope: "peeq", scope_live: false }]);
    render(<MemoryPage onOpenThread={() => {}} />);
    const pill = await screen.findByText("peeq");
    expect(pill.getAttribute("title")).toContain("applies everywhere");
  });

  it("forgets a rule and takes it off the list", async () => {
    const mock = listing(true, [rule]);
    const onCount = vi.fn();
    render(<MemoryPage onOpenThread={() => {}} onCount={onCount} />);
    await screen.findByText("Never draw flowchart diagrams.");

    fireEvent.click(screen.getByRole("button", { name: 'Forget "Never draw flowchart diagrams."' }));

    await waitFor(() => expect(screen.queryByText("Never draw flowchart diagrams.")).toBeNull());
    expect(mock).toHaveBeenCalledWith("/api/memory/7", { method: "DELETE" });
    // onCount fires from an effect, which the scheduler may run in a later task
    // than the render that drops the row. Asserting it straight after the
    // waitFor above raced that effect and went red on a slow runner.
    await waitFor(() => expect(onCount).toHaveBeenLastCalledWith(0));
  });

  it("says so when the list cannot be fetched", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 500, json: async () => ({}) })));
    render(<MemoryPage onOpenThread={() => {}} />);
    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});
