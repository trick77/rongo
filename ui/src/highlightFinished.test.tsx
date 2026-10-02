import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { highlightBlock, highlightFinished } from "./highlight";

describe("highlightFinished", () => {
  it("hands back the very nodes it made for the same block", () => {
    // Given a closed block that has been coloured once
    const code = "func A() int { return 1 }";
    const first = highlightFinished(code, "go");

    // When the answer is parsed again, as it is for every token after it
    const again = highlightFinished(code, "go");

    // Then nothing is coloured twice, and it is what highlightBlock makes
    expect(again).toBe(first);
    const { container: a } = render(<code>{first}</code>);
    const { container: b } = render(<code>{highlightBlock(code, "go")}</code>);
    expect(a.innerHTML).toBe(b.innerHTML);
  });

  it("keeps blocks of different text or language apart", () => {
    const go = highlightFinished("x := 1", "go");

    expect(highlightFinished("x := 2", "go")).not.toBe(go);
    expect(highlightFinished("x := 1", "typescript")).not.toBe(go);
    expect(highlightFinished("plain", null)).toEqual(["plain"]);
  });

  it("forgets the oldest block rather than growing without end", () => {
    const first = highlightFinished("first := 0", "go");

    for (let i = 0; i < 80; i++) highlightFinished(`v${i} := ${i}`, "go");

    expect(highlightFinished("first := 0", "go")).not.toBe(first);
  });
});
