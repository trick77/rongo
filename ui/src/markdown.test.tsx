import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import Markdown from "./markdown";

describe("Markdown", () => {
  it("macht aus # eine Ueberschrift und nicht aus dem Zeichen selbst Text", () => {
    const { container } = render(<Markdown text={"# Der Mechanismus\n\nEin Absatz."} />);
    expect(container.querySelector("h2")?.textContent).toBe("Der Mechanismus");
    expect(container.textContent).not.toContain("#");
  });

  it("setzt **fett** und `Code` als Auszeichnung", () => {
    const { container } = render(<Markdown text={"Der **Job** ruft `SendTeaser` auf."} />);
    expect(container.querySelector("strong")?.textContent).toBe("Job");
    expect(container.querySelector("code")?.textContent).toBe("SendTeaser");
    expect(container.textContent).toBe("Der Job ruft SendTeaser auf.");
  });

  it("rendert einen Codeblock als Block, nicht als Absatz voller Backticks", () => {
    const { container } = render(
      <Markdown text={"Vorher.\n\n```go\nfunc main() {}\n```\n\nNachher."} />,
    );
    expect(container.querySelector("pre code")?.textContent).toBe("func main() {}");
    expect(container.textContent).not.toContain("```");
  });

  it("rendert Aufzaehlungen als Liste", () => {
    const { container } = render(<Markdown text={"- eins\n- zwei\n"} />);
    expect(container.querySelectorAll("li").length).toBe(2);
    expect(container.querySelectorAll("li")[1].textContent).toBe("zwei");
  });

  // The named attack. Every claim in an answer carries a marker; a renderer
  // that treats brackets as syntax — link parsing, "tidy up stray brackets" —
  // deletes the evidence trail while looking like it worked.
  describe("Belegmarken", () => {
    it("bleiben im Fliesstext sichtbar", () => {
      render(<Markdown text={"Der Versand laeuft ueber einen Job [1], gestartet vom Scheduler [12]."} />);
      expect(screen.getByText(/\[1\]/)).toBeTruthy();
      expect(screen.getByText(/\[12\]/)).toBeTruthy();
    });

    it("bleiben in fettem Text sichtbar", () => {
      const { container } = render(<Markdown text={"**Nur der Job [3] schreibt.**"} />);
      expect(container.querySelector("strong")?.textContent).toBe("Nur der Job [3] schreibt.");
    });

    it("bleiben neben Inline-Code sichtbar", () => {
      const { container } = render(<Markdown text={"`SendTeaser` [7] wird aufgerufen."} />);
      expect(container.textContent).toBe("SendTeaser [7] wird aufgerufen.");
    });

    it("bleiben in einer Ueberschrift sichtbar", () => {
      const { container } = render(<Markdown text={"## Der Versand [2]"} />);
      expect(container.querySelector("h3")?.textContent).toBe("Der Versand [2]");
    });
  });

  // The answer is re-rendered on every streamed token, so half-written markup
  // is the normal case here, not an edge case.
  describe("waehrend des Streamens", () => {
    it("verschluckt einen noch offenen Codeblock nicht", () => {
      const { container } = render(<Markdown text={"Text.\n\n```go\nfunc main() {"} />);
      expect(container.querySelector("pre code")?.textContent).toBe("func main() {");
      expect(container.textContent).toContain("Text.");
    });

    it("zeigt ein angefangenes **fett als Text, statt den Rest zu schlucken", () => {
      const { container } = render(<Markdown text={"Der **Job ruft"} />);
      expect(container.textContent).toBe("Der **Job ruft");
    });

    it("zeigt ein angefangenes `Code als Text", () => {
      const { container } = render(<Markdown text={"Der `SendTeas"} />);
      expect(container.textContent).toBe("Der `SendTeas");
    });
  });
});
