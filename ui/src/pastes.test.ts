import { describe, it, expect } from "vitest";
import {
  PASTE_CHAR_THRESHOLD,
  PASTE_LINE_THRESHOLD,
  MAX_QUESTION_BYTES,
  byteLength,
  countLines,
  fold,
  shouldCollapse,
  stagePaste,
  strip,
} from "./pastes";

describe("shouldCollapse", () => {
  it("leaves a paragraph inline and folds a wall", () => {
    expect(shouldCollapse("a".repeat(PASTE_CHAR_THRESHOLD))).toBe(false);
    expect(shouldCollapse("a".repeat(PASTE_CHAR_THRESHOLD + 1))).toBe(true);
  });

  it("folds on lines as well as on length", () => {
    expect(shouldCollapse(Array(PASTE_LINE_THRESHOLD).fill("x").join("\n"))).toBe(false);
    expect(shouldCollapse(Array(PASTE_LINE_THRESHOLD + 1).fill("x").join("\n"))).toBe(true);
  });

  it("does not count the trailing newline every clipboard carries", () => {
    expect(shouldCollapse(Array(PASTE_LINE_THRESHOLD).fill("x").join("\n") + "\n")).toBe(false);
  });
});

describe("countLines", () => {
  it("counts lines, not newlines", () => {
    expect(countLines("one")).toBe(1);
    expect(countLines("one\ntwo\nthree")).toBe(3);
  });
});

describe("byteLength", () => {
  it("measures UTF-8 bytes, which is what the server counts", () => {
    expect(byteLength("ä")).toBe(2);
    expect(byteLength("a")).toBe(1);
    expect(MAX_QUESTION_BYTES).toBe(32768);
  });
});

describe("stagePaste", () => {
  it("trims the block and counts its lines", () => {
    expect(stagePaste("  panic: boom\nmain.go:12\n\n")).toEqual({ text: "panic: boom\nmain.go:12", lines: 2 });
  });
});

describe("fold and strip", () => {
  const trace = { text: "panic: boom\nmain.go:12", lines: 2 };
  const log = { text: "[info] started\n[warn] slow", lines: 2 };

  it("joins the typed text and the pastes with a blank line", () => {
    expect(fold("Why? ", [trace])).toBe("Why?\n\npanic: boom\nmain.go:12");
  });

  it("folds a paste-only question", () => {
    expect(fold("   ", [trace])).toBe(trace.text);
    expect(fold("", [])).toBe("");
  });

  it("strips what fold joined, in order", () => {
    const q = fold("Why?", [trace, log]);
    expect(strip(q, [trace, log])).toEqual({ typed: "Why?", matched: [true, true] });
  });

  it("strips a paste-only question down to nothing", () => {
    expect(strip(fold("", [trace]), [trace])).toEqual({ typed: "", matched: [true] });
  });

  it("leaves a block inline when it is not where fold put it", () => {
    // Never drawn twice: a block that does not match the tail stays in the
    // prose and gets no chip.
    expect(strip("Why?\n\npanic: boom\nmain.go:12", [log])).toEqual({
      typed: "Why?\n\npanic: boom\nmain.go:12",
      matched: [false],
    });
  });

  it("stops at the first block that does not match, from the tail", () => {
    const q = fold("Why?", [trace, log]);
    expect(strip(q, [log, trace])).toEqual({ typed: q, matched: [false, false] });
  });

  it("copes with a question the server trimmed", () => {
    expect(strip(fold("Why?", [trace]).trim(), [trace])).toEqual({ typed: "Why?", matched: [true] });
  });
});
