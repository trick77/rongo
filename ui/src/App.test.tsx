import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => vi.unstubAllGlobals());

describe("App", () => {
  it("renders the heading with text rongo", () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => [] })));
    render(<App />);
    const heading = screen.getByRole("heading", { level: 1 });
    expect(heading.textContent).toBe("rongo");
  });

  it("behaelt den laufenden Thread beim Wechsel auf Repos", async () => {
    // Unmounting Ask would drop the answer on screen while the stream keeps
    // writing into a dead component, and nothing in the UI can fetch the thread
    // back yet.
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => [] })));
    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByLabelText("Frage"), "Eine Frage, die stehen bleiben muss");

    await user.click(screen.getByRole("button", { name: "Repos" }));
    await user.click(screen.getByRole("button", { name: "Fragen" }));

    expect((screen.getByLabelText("Frage") as HTMLTextAreaElement).value).toBe(
      "Eine Frage, die stehen bleiben muss",
    );
  });
});
