import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { highlightBlock, highlightLines, languageForPath, languageOf } from "./highlight";

describe("languageOf", () => {
  it("accepts a registered grammar and rejects the rest", () => {
    expect(languageOf("go")).toBe("go");
    expect(languageOf("TypeScript")).toBe("typescript");
    expect(languageOf("nosuchlang")).toBeNull();
    expect(languageOf("")).toBeNull();
    expect(languageOf(undefined)).toBeNull();
  });
});

describe("languageForPath", () => {
  it("maps the extension or a well-known basename", () => {
    expect(languageForPath("backend/internal/sched/sched.go")).toBe("go");
    expect(languageForPath("ui/src/Ask.tsx")).toBe("typescript");
    expect(languageForPath("Makefile")).toBe("makefile");
    expect(languageForPath("Containerfile")).toBe("dockerfile");
    expect(languageForPath("repos.yaml")).toBe("yaml");
  });

  it("returns null for what it does not know", () => {
    expect(languageForPath("LICENSE")).toBeNull();
    expect(languageForPath("data.bin.xyz")).toBeNull();
  });
});

describe("highlightBlock", () => {
  it("wraps tokens in hljs spans and keeps the text intact", () => {
    const code = 'func main() {\n\treturn "x"\n}';
    const { container } = render(<code>{highlightBlock(code, "go")}</code>);
    expect(container.querySelector(".hljs-keyword")?.textContent).toBe("func");
    expect(container.querySelector(".hljs-string")?.textContent).toBe('"x"');
    expect(container.textContent).toBe(code);
  });

  it("renders plain text without a language, and never throws", () => {
    const { container } = render(<code>{highlightBlock("[1] plain", null)}</code>);
    expect(container.querySelector("span")).toBeNull();
    expect(container.textContent).toBe("[1] plain");
  });
});

describe("highlightLines", () => {
  it("returns one entry per input line, empty lines included", () => {
    const lines = highlightLines("a\n\nb\n", "go");
    expect(lines.length).toBe(4);
    const { container } = render(<div>{lines[1]}</div>);
    expect(container.textContent).toBe("");
  });

  it("keeps a token that spans lines coloured on every line", () => {
    const code = "/* one\ntwo\nthree */\nvar x = 1";
    const lines = highlightLines(code, "go");
    for (const i of [0, 1, 2]) {
      const { container } = render(<div>{lines[i]}</div>);
      expect(container.querySelector(".hljs-comment")).not.toBeNull();
    }
    const { container } = render(<div>{lines[3]}</div>);
    expect(container.querySelector(".hljs-comment")).toBeNull();
    expect(container.querySelector(".hljs-keyword")?.textContent).toBe("var");
  });

  it("reproduces every line's text exactly", () => {
    const code = 'package x\n\n// c\nfunc F() string { return "a\\nb" }';
    const lines = highlightLines(code, "go");
    const back = lines.map((nodes) => render(<div>{nodes}</div>).container.textContent);
    expect(back).toEqual(code.split("\n"));
  });

  it("falls back to plain lines without a language", () => {
    const lines = highlightLines("x\ny", null);
    expect(lines.length).toBe(2);
    const { container } = render(<div>{lines[1]}</div>);
    expect(container.querySelector("span")).toBeNull();
    expect(container.textContent).toBe("y");
  });
});
