import { StrictMode } from "react";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Trace from "./Trace";

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

describe("Trace", () => {
  it("zeigt zusammengeklappt eine Zeile mit dem laufenden Schritt und einem Chevron", () => {
    strict(<Trace steps={["Anfrage verstehen", "Code sammeln"]} state="running" />);

    expect(screen.getByText(/Code sammeln/)).toBeTruthy();
    expect(screen.queryByText("Anfrage verstehen")).toBeNull();
    expect(screen.getByRole("button").querySelector("svg")).toBeTruthy();
  });

  it("zeigt aufgeklappt jeden Schritt als Knoten auf einer durchgehenden Linie", async () => {
    const user = userEvent.setup();
    strict(<Trace steps={["Anfrage verstehen", "Code sammeln"]} state="running" />);

    await user.click(screen.getByRole("button"));

    expect(screen.getByText("Anfrage verstehen")).toBeTruthy();
    expect(screen.getByText("Code sammeln")).toBeTruthy();
  });

  it("endet bei einem fertigen Zug mit dem Fertig-Knoten", async () => {
    const user = userEvent.setup();
    strict(<Trace steps={["Anfrage verstehen"]} state="done" />);

    await user.click(screen.getByRole("button"));

    expect(screen.getByText("Fertig")).toBeTruthy();
    expect(screen.queryByText("Wartet auf Auswahl")).toBeNull();
  });

  it("endet bei einer Klaerfrage mit dem ockerfarbenen Warte-Knoten, nicht dem Haken", async () => {
    // loom has no third state: complete = !active && !streaming would claim
    // "done" while a person is still being waited on.
    const user = userEvent.setup();
    strict(<Trace steps={["Anfrage verstehen"]} state="waiting" />);

    await user.click(screen.getByRole("button"));

    const waiting = screen.getByText("Wartet auf Auswahl");
    expect(waiting).toBeTruthy();
    expect(screen.queryByText("Fertig")).toBeNull();
    const marker = waiting.closest("li")?.querySelector("span[aria-hidden]");
    expect(marker?.getAttribute("class")).toContain("ochre");
  });

  it("wird als Panel hoeflich angesagt", () => {
    strict(<Trace steps={["Anfrage verstehen"]} state="running" />);

    const status = screen.getByRole("status");
    expect(status.getAttribute("aria-live")).toBe("polite");
  });
});
