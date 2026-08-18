import { StrictMode } from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Clarify from "./Clarify";

// Every test mounts under StrictMode, the way the real app renders it.
const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

const candidates = [
  {
    idx: 0,
    title: "Ueber den Login-Service",
    summary: "Die Anmeldung laeuft ueber den zentralen Login-Service.",
    repo: "peeq",
    branch: "master",
  },
  {
    idx: 1,
    title: "Ueber den Legacy-Adapter",
    summary: "Der alte Adapter meldet Nutzer direkt gegen LDAP an.",
    repo: "peeq-legacy",
    branch: "release-2024.3",
  },
];

describe("Clarify", () => {
  it("nennt jeden Kandidaten mit Repo und einer Zeile", () => {
    strict(<Clarify candidates={candidates} onChoose={() => {}} />);

    expect(screen.getByText("Ueber den Login-Service")).toBeTruthy();
    expect(screen.getByText("peeq · master")).toBeTruthy();
    expect(screen.getByText(/zentralen Login-Service/)).toBeTruthy();

    expect(screen.getByText("Ueber den Legacy-Adapter")).toBeTruthy();
    expect(screen.getByText("peeq-legacy · release-2024.3")).toBeTruthy();
    expect(screen.getByText(/LDAP an/)).toBeTruthy();
  });

  it("meldet beim Waehlen den Index des Kandidaten zurueck", async () => {
    const onChoose = vi.fn();
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} onChoose={onChoose} />);

    await user.click(screen.getByText("Ueber den Legacy-Adapter"));

    expect(onChoose).toHaveBeenCalledWith(1);
  });

  it("klappt die Karte nach der Wahl auf eine Zeile zusammen und bleibt im Thread", () => {
    // The decision belongs in the record: sometimes you only notice from the
    // answer that you picked the wrong mechanism, which is why the card
    // stays — just collapsed.
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={() => {}} />);

    expect(screen.getByText(/Gewählt: Ueber den Login-Service/)).toBeTruthy();
    expect(screen.queryByText(/zentralen Login-Service/)).toBeNull();
    expect(screen.queryByText("Ueber den Legacy-Adapter")).toBeNull();
  });

  it("zeigt beim Aufklappen der zusammengeklappten Karte jeden Kandidaten, den gewaehlten markiert", async () => {
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={() => {}} />);

    await user.click(screen.getByRole("button", { name: /Gewählt/ }));

    expect(screen.getByText("Ueber den Login-Service")).toBeTruthy();
    expect(screen.getByText("Ueber den Legacy-Adapter")).toBeTruthy();
    const chosenButton = screen.getByText("Ueber den Login-Service").closest("button");
    expect(chosenButton?.textContent).toContain("Gewählt");
  });

  it("laesst einen anderen Kandidaten nach der Wahl anklickbar — er startet einen neuen Zug", async () => {
    const onChoose = vi.fn();
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={onChoose} />);

    await user.click(screen.getByRole("button", { name: /Gewählt/ }));
    await user.click(screen.getByText("Ueber den Legacy-Adapter"));

    expect(onChoose).toHaveBeenCalledWith(1);
  });

  it("dreht den Chevron beim Oeffnen um 90 Grad, ohne das Glyph zu tauschen", async () => {
    // AGENTS.md: chevron only — no triangle, no plus/minus, no glyph swap.
    const user = userEvent.setup();
    strict(<Clarify candidates={candidates} chosenIdx={0} onChoose={() => {}} />);

    const button = screen.getByRole("button", { name: /Gewählt/ });
    const pathBefore = button.querySelector("svg path")?.getAttribute("d");
    expect(button.querySelector("svg")?.getAttribute("class")).not.toContain("rotate-90");

    await user.click(button);

    const pathAfter = button.querySelector("svg path")?.getAttribute("d");
    expect(button.querySelector("svg")?.getAttribute("class")).toContain("rotate-90");
    expect(pathAfter).toBe(pathBefore);
  });
});
