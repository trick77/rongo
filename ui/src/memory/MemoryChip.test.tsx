import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import MemoryChip from "./MemoryChip";
import type { TurnMemory } from "../turns";

afterEach(() => vi.unstubAllGlobals());

const kept: TurnMemory = {
  id: 7,
  text: "Never draw flowchart diagrams.",
  scope: "",
  replaced: [],
  removed: [],
  scopeDropped: "",
};

describe("MemoryChip", () => {
  it("says what was remembered, with a way back", () => {
    render(<MemoryChip memory={kept} />);
    expect(screen.getByText("Remembered")).toBeTruthy();
    expect(screen.getByText("Never draw flowchart diagrams.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Undo" })).toBeTruthy();
  });

  it("undo deletes the rule and says so, once", async () => {
    const mock = vi.fn(async () => ({ ok: true, status: 204 }));
    vi.stubGlobal("fetch", mock);
    render(<MemoryChip memory={kept} />);

    fireEvent.click(screen.getByRole("button", { name: "Undo" }));

    await screen.findByText("Forgotten");
    expect(mock).toHaveBeenCalledWith("/api/memory/7", { method: "DELETE" });
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("keeps the rule when the server refuses", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 500 })));
    render(<MemoryChip memory={kept} />);

    fireEvent.click(screen.getByRole("button", { name: "Undo" }));

    await waitFor(() => expect(screen.getByRole("button", { name: "Undo" })).toBeTruthy());
    expect(screen.queryByText("Forgotten")).toBeNull();
  });

  it("names what the rule replaced and where it holds", () => {
    render(<MemoryChip memory={{ ...kept, scope: "shop", replaced: ["Always draw one."] }} />);
    expect(screen.getByText("shop")).toBeTruthy();
    expect(screen.getByText(/replaces "Always draw one\."/)).toBeTruthy();
  });

  it("says a scope the index does not carry applies everywhere", () => {
    render(<MemoryChip memory={{ ...kept, scopeDropped: "warehouse" }} />);
    expect(screen.getByText(/warehouse is not indexed, so it applies everywhere/)).toBeTruthy();
  });

  it("a turn that only forgot has nothing to undo", () => {
    render(<MemoryChip memory={{ ...kept, id: null, text: "", removed: ["Keep it short."] }} />);
    expect(screen.getByText(/Forgotten/)).toBeTruthy();
    expect(screen.getByText(/"Keep it short\."/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("read-only shows the rule and no undo", () => {
    render(<MemoryChip memory={kept} readOnly />);
    expect(screen.getByText("Never draw flowchart diagrams.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });
});
