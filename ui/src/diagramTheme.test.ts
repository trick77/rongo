import { describe, it, expect } from "vitest";
import { palette, textWidth, wrap } from "./diagramTheme";

describe("palette", () => {
  it("reads every role off the document, and keeps a var() for a token it lacks", () => {
    document.documentElement.style.setProperty("--diagram-surface", "#192026");
    const p = palette();
    expect(p.surface).toBe("#192026");
    expect(p.warn).toBe("var(--diagram-warn)");
    document.documentElement.style.removeProperty("--diagram-surface");
  });
});

describe("textWidth", () => {
  it("costs a wide character 1em and a mark nothing", () => {
    expect(textWidth("ab", 10, "mono")).toBe(12);
    expect(textWidth("결제", 10, "sans")).toBe(20);
    expect(textWidth("é", 10, "mono")).toBe(6);
  });
});

describe("wrap", () => {
  it("breaks at spaces within the width and keeps the label's own breaks", () => {
    expect(wrap("Schwellenwert überschritten?", 100, 13, "sans")).toEqual([
      "Schwellenwert",
      "überschritten?",
    ]);
    expect(wrap("one\ntwo", 500, 13, "sans")).toEqual(["one", "two"]);
    expect(wrap("short", 500, 13, "sans")).toEqual(["short"]);
  });
});
