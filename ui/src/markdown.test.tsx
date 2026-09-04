import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import Markdown from "./markdown";

describe("Markdown", () => {
  it("turns # into a heading rather than rendering the character itself", () => {
    const { container } = render(<Markdown text={"# The mechanism\n\nA paragraph."} />);
    expect(container.querySelector("h2")?.textContent).toBe("The mechanism");
    expect(container.textContent).not.toContain("#");
  });

  it("renders **bold** and `code` as markup", () => {
    const { container } = render(<Markdown text={"The **job** calls `SendTeaser`."} />);
    expect(container.querySelector("strong")?.textContent).toBe("job");
    expect(container.querySelector("code")?.textContent).toBe("SendTeaser");
    expect(container.textContent).toBe("The job calls SendTeaser.");
  });

  it("renders a code block as a block, not a paragraph full of backticks", () => {
    const { container } = render(
      <Markdown text={"Before.\n\n```go\nfunc main() {}\n```\n\nAfter."} />,
    );
    expect(container.querySelector("pre code")?.textContent).toBe("func main() {}");
    expect(container.textContent).not.toContain("```");
  });

  describe("code block colouring", () => {
    it("colours a fence by its language tag, text unchanged", () => {
      const { container } = render(<Markdown text={"```go\nfunc main() {}\n```"} />);
      expect(container.querySelector("pre code .hljs-keyword")?.textContent).toBe("func");
      expect(container.querySelector("pre code")?.textContent).toBe("func main() {}");
    });

    it("leaves an untagged or unknown fence as plain text", () => {
      for (const src of ["```\nfunc main() {}\n```", "```nosuch\nfunc main() {}\n```"]) {
        const { container } = render(<Markdown text={src} />);
        expect(container.querySelector("pre code span")).toBeNull();
        expect(container.querySelector("pre code")?.textContent).toBe("func main() {}");
      }
    });

    it("keeps a marker-shaped expression inside code literal", () => {
      const { container } = render(<Markdown text={"```go\nx := a[1]\n```"} />);
      expect(container.querySelector("sup")).toBeNull();
      expect(container.querySelector("pre code")?.textContent).toBe("x := a[1]");
    });
  });

  it("renders bullet points as a list", () => {
    const { container } = render(<Markdown text={"- one\n- two\n"} />);
    expect(container.querySelectorAll("li").length).toBe(2);
    expect(container.querySelectorAll("li")[1].textContent).toBe("two");
  });

  // The named attack. Every claim in an answer carries a marker; a renderer
  // that treats brackets as syntax — link parsing, "tidy up stray brackets" —
  // deletes the evidence trail while looking like it worked.
  describe("citation markers", () => {
    it("stay visible in running text", () => {
      const { container } = render(
        <Markdown text={"Shipping runs through a job [1], started by the scheduler [12]."} />,
      );
      expect(container.textContent).toBe("Shipping runs through a job [1], started by the scheduler [12].");
    });

    it("are styled as superscripts, never consumed", () => {
      const { container } = render(<Markdown text={"A job [1] runs."} />);
      expect(container.querySelector("sup")?.textContent).toBe("[1]");
      expect(container.textContent).toBe("A job [1] runs.");
    });

    it("are only drawn as citations once a source is known to back them", () => {
      // The backend drops an invented marker from the citation list, never
      // from the text. A chip for [7] with no row 7 would look checkable.
      const { container } = render(<Markdown text={"Real [1], invented [7]."} backed={new Set([1])} />);
      const sups = container.querySelectorAll("sup");
      expect(sups.length).toBe(1);
      expect(sups[0].textContent).toBe("[1]");
      expect(container.textContent).toBe("Real [1], invented [7].");
    });

    it("look the same while streaming as once the citations have arrived", () => {
      // The citations event is the last thing before done. Markers that
      // changed colour all at once at that moment read as a glitch.
      const streaming = render(<Markdown text={"A job [1] runs."} />);
      const done = render(<Markdown text={"A job [1] runs."} backed={new Set([1])} />);
      expect(streaming.container.querySelector("sup")?.className).toBe(
        done.container.querySelector("sup")?.className,
      );
    });

    it("stay plain text while the closing bracket has not arrived yet", () => {
      const { container } = render(<Markdown text={"A job [1"} />);
      expect(container.querySelector("sup")).toBeNull();
      expect(container.textContent).toBe("A job [1");
    });

    it("stay visible in bold text", () => {
      const { container } = render(<Markdown text={"**Only the job [3] writes.**"} />);
      expect(container.querySelector("strong")?.textContent).toBe("Only the job [3] writes.");
    });

    it("stay visible next to inline code", () => {
      const { container } = render(<Markdown text={"`SendTeaser` [7] is called."} />);
      expect(container.textContent).toBe("SendTeaser [7] is called.");
    });

    it("stay visible in a heading", () => {
      const { container } = render(<Markdown text={"## Shipping [2]"} />);
      expect(container.querySelector("h3")?.textContent).toBe("Shipping [2]");
    });
  });

  // The answer is re-rendered on every streamed token, so half-written markup
  // is the normal case here, not an edge case.
  describe("while streaming", () => {
    it("does not swallow a code block that is still open", () => {
      const { container } = render(<Markdown text={"Text.\n\n```go\nfunc main() {"} />);
      expect(container.querySelector("pre code")?.textContent).toBe("func main() {");
      expect(container.querySelector("pre code .hljs-keyword")?.textContent).toBe("func");
      expect(container.textContent).toContain("Text.");
    });

    it("shows a half-typed **bold as text instead of swallowing the rest", () => {
      const { container } = render(<Markdown text={"The **job calls"} />);
      expect(container.textContent).toBe("The **job calls");
    });

    it("shows a half-typed `code as text", () => {
      const { container } = render(<Markdown text={"The `SendTeas"} />);
      expect(container.textContent).toBe("The `SendTeas");
    });
  });
});
