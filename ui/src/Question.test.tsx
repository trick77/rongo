import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Question from "./Question";

/**
 * jsdom lays nothing out: every box measures zero, so a question is never
 * clipped there and the fold would never appear in a test. These give the text
 * box the heights a browser would give it — clipped when scrollHeight runs
 * past the clamp, not clipped when it fits.
 */
function heights(clipped: boolean) {
  const box = document.querySelector(".whitespace-pre-wrap") as HTMLElement;
  Object.defineProperty(box, "scrollHeight", { configurable: true, value: clipped ? 400 : 60 });
  Object.defineProperty(box, "clientHeight", { configurable: true, value: 60 });
  return box;
}

const long = "Why is indexing slow?\n\nIt takes six minutes on a cold cache.\n\nIs that the embeddings?";

describe("Question", () => {
  it("keeps the breaks the reader typed", () => {
    render(<Question text={long} />);
    // One node, its newlines intact: a plain <p> collapsed them into an
    // unbroken run, which is what a multi-paragraph question came back as.
    expect(screen.getByText(long, { collapseWhitespace: false })).toBeTruthy();
  });

  it("folds a long question and gives it back on a click", async () => {
    const { rerender } = render(<Question text={long} />);
    heights(true);
    rerender(<Question text={long + " "} />);

    const toggle = await screen.findByRole("button", { name: "Show the whole question" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    // The text is in the DOM the whole time — the fold is a clamp, not a
    // truncation — so copy, export and find-in-page never lose it.
    expect(document.querySelector(".max-h-\\[4\\.5em\\]")).toBeTruthy();

    await userEvent.setup().click(toggle);

    expect(document.querySelector(".max-h-\\[4\\.5em\\]")).toBeNull();
    expect(screen.getByRole("button", { name: "Show less" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("folds again when another thread's question arrives", async () => {
    // Turns are keyed by their position, so switching threads hands the same
    // instance a different question. An unfolded one used to leave the next
    // one unfolded, with a "Show less" under a single line.
    const { rerender } = render(<Question text={long} />);
    heights(true);
    rerender(<Question text={long + " "} />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Show the whole question" }));

    heights(false);
    rerender(<Question text="How does shipping work?" />);

    expect(screen.queryByRole("button")).toBeNull();
  });

  it("offers no fold when nothing is behind it", () => {
    const { rerender } = render(<Question text="How does shipping work?" />);
    heights(false);
    rerender(<Question text="How does shipping work? " />);

    expect(screen.queryByRole("button")).toBeNull();
  });
});
