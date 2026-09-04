import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Markdown, { splitIntoSegments } from "./markdown";

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

    it("open their source on click, once a source is known to back them", async () => {
      // Hover only points at the pane, and a tablet has no hover and no
      // pane; a chip that cannot be tapped leaves the reader no way to a
      // source but a collapsed list under the answer.
      const onOpen = vi.fn();
      const { container } = render(
        <Markdown text={"Real [12], invented [7]."} backed={new Set([12])} onMarkerOpen={onOpen} />,
      );
      const buttons = container.querySelectorAll("sup button");
      expect(buttons.length).toBe(1);
      await userEvent.click(buttons[0]);
      expect(onOpen).toHaveBeenCalledWith(12);
      expect(container.textContent).toBe("Real [12], invented [7].");
    });

    it("are not buttons while the citations are still unknown", () => {
      // Nothing to open yet: the list arrives last.
      const { container } = render(<Markdown text={"A job [1] runs."} onMarkerOpen={() => {}} />);
      expect(container.querySelector("sup")).toBeTruthy();
      expect(container.querySelector("sup button")).toBeNull();
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

    it("draw one chip per number of a grouped marker", () => {
      // A claim resting on several sources comes out as [1, 2]. One chip
      // per number keeps every source reachable, and each chip is a whole
      // marker on its own, so a copy reads "[1], [12]".
      const { container } = render(<Markdown text={"Compared on poll [1, 12]."} />);
      const sups = container.querySelectorAll("sup");
      expect(sups.length).toBe(2);
      expect(sups[0].textContent).toBe("[1]");
      expect(sups[1].textContent).toBe("[12]");
      expect(container.textContent).toBe("Compared on poll [1], [12].");
    });

    it("drop only the invented number of a grouped marker back to text", () => {
      const { container } = render(
        <Markdown text={"Compared on poll [1, 9]."} backed={new Set([1])} />,
      );
      const sups = container.querySelectorAll("sup");
      expect(sups.length).toBe(1);
      expect(sups[0].textContent).toBe("[1]");
      expect(container.textContent).toBe("Compared on poll [1], [9].");
    });

    it("leave a grouped marker verbatim when nothing backs any of it", () => {
      // Answers stored before groups were read have no citation rows for
      // their numbers; their text must not change shape on re-render.
      const { container } = render(
        <Markdown text={"Compared on poll [78, 139]."} backed={new Set([1])} />,
      );
      expect(container.querySelector("sup")).toBeNull();
      expect(container.textContent).toBe("Compared on poll [78, 139].");
    });

    it("stay plain text while a grouped marker is still being written", () => {
      const { container } = render(<Markdown text={"Compared on poll [1, "} />);
      expect(container.querySelector("sup")).toBeNull();
      expect(container.textContent).toBe("Compared on poll [1, ");
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

  // Newly arrived text appears once, dim to full, as ../loom's sidebar-less
  // answer does. The split is the visible unit of that fade.
  describe("the streaming fade", () => {
    it("cuts prose into segments at a clause end or a length", () => {
      expect(splitIntoSegments("Short one. Then a much longer run of words here.")).toEqual([
        "Short one. ",
        "Then a much longer run of words ",
        "here.",
      ]);
    });

    it("keeps every character of the text it splits", () => {
      const src = "The  job\nruns, and then it stops.";
      expect(splitIntoSegments(src).join("")).toBe(src);
    });

    it("wraps prose in segments without changing what the text reads", () => {
      const { container } = render(<Markdown text={"The job runs, and then it stops."} />);
      const segs = container.querySelectorAll(".stream-seg");
      expect(segs.length).toBeGreaterThan(1);
      expect(container.textContent).toBe("The job runs, and then it stops.");
    });

    it("leaves code and citation chips out of the fade", () => {
      // A chip or a link that re-faded on every token would flicker, and a
      // segment span inside a highlighted block would fight the grammar.
      const { container } = render(
        <Markdown text={"Calls `Send` [1] here.\n\n```go\nx := 1\n```"} backed={new Set([1])} />,
      );
      expect(container.querySelectorAll(".stream-seg").length).toBeGreaterThan(0);
      expect(container.querySelector("code .stream-seg")).toBeNull();
      expect(container.querySelector("pre .stream-seg")).toBeNull();
      expect(container.querySelector("sup .stream-seg")).toBeNull();
      expect(container.querySelector("sup")?.textContent).toBe("[1]");
    });

    it("does not fade bold, which the reader has already seen unbolded", () => {
      // Until the closing ** arrives the words are on screen as the literal
      // text they came as. The <strong> that replaces them is a new element,
      // so segments inside it would mount and start their fade at that
      // moment, dropping a phrase the reader has read back to a fifth of its
      // brightness. Confirmed in the browser before this was written.
      const { container, rerender } = render(<Markdown text={"The job **runs fast"} />);
      expect(container.textContent).toBe("The job **runs fast");
      rerender(<Markdown text={"The job **runs fast** now."} />);
      expect(container.querySelector("strong")?.textContent).toBe("runs fast");
      expect(container.querySelector("strong .stream-seg")).toBeNull();
      expect(container.textContent).toBe("The job runs fast now.");
    });

    it("keeps the segments already on screen when more text arrives", () => {
      // The whole answer re-renders on every token. If a settled segment
      // remounted, its fade would restart and the finished text would
      // flicker for the whole length of the answer.
      const { container, rerender } = render(<Markdown text={"The job runs, and then"} />);
      const first = container.querySelector(".stream-seg");
      expect(first).not.toBeNull();
      rerender(<Markdown text={"The job runs, and then it stops. A second sentence."} />);
      expect(container.querySelector(".stream-seg")).toBe(first);
      expect(container.querySelectorAll(".stream-seg").length).toBeGreaterThan(2);
    });
  });
});
