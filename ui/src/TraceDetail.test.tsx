import { StrictMode } from "react";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import Trace from "./Trace";

const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);
const t0 = 1_000_000;

describe("Trace, what each step found", () => {
  it("draws the understanding's terms, identifiers and scope under its step", () => {
    strict(
      <Trace
        steps={[
          {
            step: "understanding",
            at: t0,
            detail: {
              terms: ["Vorerfassung abschicken"],
              code_terms: ["ControllerVorerfassung", "TOPIC_VORERFASSUNG"],
              repos: ["policenantrag-service"],
            },
          },
        ]}
        state="running"
        startedAt={t0}
      />,
    );
    expect(screen.getByText("Vorerfassung abschicken")).toBeTruthy();
    expect(screen.getByText("ControllerVorerfassung")).toBeTruthy();
    expect(screen.getByText(/policenantrag-service, named by the question/)).toBeTruthy();
  });

  it("says a rework reads the previous answer, never that it scoped every project", () => {
    strict(
      <Trace
        steps={[{ step: "understanding", at: t0, detail: { intent: "rework" } }]}
        state="running"
        startedAt={t0}
      />,
    );
    expect(screen.getByText(/the previous answer from its own sources/)).toBeTruthy();
    expect(screen.queryByText(/every indexed project/)).toBeNull();
  });

  it("draws a changes turn's search as commits in a window, with the topic", () => {
    strict(
      <Trace
        steps={[
          {
            step: "searching",
            at: t0,
            detail: { commits: 7, since_days: 2, topic: "snapshot handling", per_repo: { rongo: 7 } },
          },
        ]}
        state="running"
        startedAt={t0}
      />,
    );
    const detail = document.querySelector(".trace-detail");
    expect(detail?.textContent).toContain("7 commits");
    expect(detail?.textContent).toContain("in the last 2 days");
    expect(screen.getByText("snapshot handling")).toBeTruthy();
    expect(screen.getByText("rongo 7")).toBeTruthy();
    expect(detail?.textContent).not.toContain("hits");
  });

  it("draws a release turn's search as commits between two stages, with the verdicts", () => {
    strict(
      <Trace
        steps={[
          {
            step: "searching",
            at: t0,
            detail: {
              commits: 5,
              between: ["prod", "test"],
              infrastructure: "shop-infra",
              images: 3,
              per_repo: { "shop-backend": 5 },
              notes: { unchanged: 1, undeclared: 1 },
            },
          },
        ]}
        state="running"
        startedAt={t0}
      />,
    );
    const detail = document.querySelector(".trace-detail");
    expect(detail?.textContent).toContain("5 commits");
    expect(detail?.textContent).toContain("between prod and test");
    expect(detail?.textContent).toContain("3 images");
    expect(screen.getByText("shop-infra")).toBeTruthy();
    expect(screen.getByText("shop-backend 5")).toBeTruthy();
    expect(screen.getByText("unchanged 1")).toBeTruthy();
    expect(detail?.textContent).not.toContain("in the last");
  });

  it("turns the routing rung into a sentence, never its identifier", () => {
    strict(
      <Trace
        steps={[{ step: "routing", at: t0, detail: { decision: "ask", rung: "repository", candidates: ["peeq", "rongo"] } }]}
        state="waiting"
        startedAt={t0}
        endedAt={t0 + 100}
      />,
    );
    expect(screen.getByText(/Asking back: the matches span several projects/)).toBeTruthy();
    expect(screen.queryByText(/repository/)).toBeNull();
    expect(screen.getByText("peeq")).toBeTruthy();
    expect(screen.getByText(/decided by rule, no model/)).toBeTruthy();
  });

  it("says how the code was read and which boundary was crossed", () => {
    strict(
      <Trace
        steps={[
          {
            step: "gathering",
            at: t0,
            detail: {
              hits: 20,
              references: 31,
              crossings: 4,
              sources: 55,
              repos: 2,
              tokens: 22000,
              budget: 24000,
              crossed: [{ from: "shipping", to: "queue-master", via: "destination shipping-task" }],
            },
          },
        ]}
        state="running"
        startedAt={t0}
      />,
    );
    expect(screen.getByText("+31 referenced")).toBeTruthy();
    expect(screen.getByText("+4 across a boundary")).toBeTruthy();
    expect(screen.getByText(/55 sources in 2 repositories/)).toBeTruthy();
    expect(screen.getByText(/22.0k of 24.0k tokens/)).toBeTruthy();
    expect(screen.getByText(/shipping → queue-master/)).toBeTruthy();
  });

  it("says how many link sites a census landed", () => {
    strict(
      <Trace
        steps={[
          {
            step: "gathering",
            at: t0,
            detail: { hits: 3, references: 2, crossings: 0, sources: 45, repos: 1, tokens: 9000, budget: 24000, link_sites: 57, links: 40 },
          },
        ]}
        state="running"
        startedAt={t0}
      />,
    );
    expect(screen.getByText("+40 of 57 link sites")).toBeTruthy();
  });

  const gathered = (locate: Record<string, unknown>) =>
    strict(
      <Trace
        steps={[
          {
            step: "gathering",
            at: t0,
            detail: { hits: 20, references: 3, crossings: 0, sources: 40, repos: 1, tokens: 9000, budget: 24000, ...locate },
          },
        ]}
        state="done"
        startedAt={t0}
        endedAt={t0 + 100}
      />,
    );

  it("says in plain words what the locate loop looked up and where it pointed the answer", () => {
    const { container } = gathered({
      locate_rounds: 3,
      locate_steps: [
        { tool: "grep", arg: "encrypt", matches: 64 },
        { tool: "grep", arg: "setAnzahlHaustiere" },
        { tool: "read", repo: "schadenmeldung-service", path: "service/src/main/java/ServiceLinkdata.java", line: 40, held: 1 },
        { tool: "symbol", arg: "LinkInfoDto", added: 2 },
        { tool: "grep", not_run: "the token budget for this step was spent" },
      ],
      locate_outcome: "pointed",
      locate_place: { repo: "schadenmeldung-service", path: "service/src/main/java/ServiceLinkdata.java", line: 52 },
    });
    const text = container.textContent ?? "";
    expect(text).toContain("Looked for the exact place in the code, 4 lookups in 3 rounds");
    expect(text).toContain('searched the code for "encrypt" — 64 matching lines');
    expect(text).toContain('searched the code for "setAnzahlHaustiere" — nothing');
    expect(text).toContain("opened ServiceLinkdata.java at line 40 — already among the sources");
    expect(text).toContain("looked up the symbol LinkInfoDto — added 2 sources");
    expect(text).toContain("not run: the token budget for this step was spent");
    expect(text).toContain(
      "Found it: ServiceLinkdata.java line 52 (schadenmeldung-service). The answer is written starting from that source.",
    );
    // The full path is on hover, never in the line.
    expect(screen.getAllByTitle("schadenmeldung-service · service/src/main/java/ServiceLinkdata.java").length).toBe(2);
  });

  it("says plainly when the locate loop did not find the place", () => {
    const { container } = gathered({
      locate_rounds: 1,
      locate_steps: [{ tool: "search", arg: "Arbeitsunfähigkeit korrigieren" }],
      locate_outcome: "not_found",
    });
    const text = container.textContent ?? "";
    expect(text).toContain('searched by meaning for "Arbeitsunfähigkeit korrigieren" — nothing');
    expect(text).toContain("Did not find the exact place. The answer uses the search results in their usual order.");
    expect(text).not.toContain("NOT FOUND");
  });

  it("says when a named place was not among the sources, or no conclusion came", () => {
    expect(
      gathered({ locate_rounds: 1, locate_steps: [{ tool: "grep", arg: "x", matches: 1 }], locate_outcome: "unpinned" })
        .container.textContent,
    ).toContain("Named a place that is not among the sources, so their order was left unchanged.");
    expect(
      gathered({ locate_rounds: 1, locate_steps: [{ tool: "grep", arg: "y", matches: 1 }] }).container.textContent,
    ).toContain("Came to no conclusion, so the sources keep their usual order.");
  });

  it("reads a trace stored with call labels and the model's sentence the same way, prose dropped", () => {
    const { container } = gathered({
      locate_rounds: 3,
      locate_calls: [
        "grep(encrypt) 64 lines",
        "grep(setAnzahlHaustiere)",
        "read(intg/extranet/application-openshift-schadenmeldung.properties:85)",
        "search(vorlage link)",
      ],
      locate_landed: ["search(vorlage link)", "found(ServiceLinkdata.java:52)"],
      locate_empty: ["grep(setAnzahlHaustiere)"],
      locate_refused: ["grep(over the call limit)", "grep(loom: outside this turn's repositories)", "grep()"],
      locate_found:
        "FOUND: schadenmeldung-service service/x/ServiceLinkdata.java:52 `public LinkInfoDto decrypt(final String base64Encoded) {` — der Vorlagen-Link wird verschlüsselt.",
    });
    const text = container.textContent ?? "";
    expect(text).toContain("4 lookups in 3 rounds");
    expect(text).toContain('searched the code for "encrypt" — 64 matching lines');
    expect(text).toContain('searched the code for "setAnzahlHaustiere" — nothing');
    expect(text).toContain("opened application-openshift-schadenmeldung.properties at line 85");
    expect(text).toContain('searched by meaning for "vorlage link" — added sources');
    // Ran, landed nothing new, found something: what it found was already held.
    expect(text).toContain("opened application-openshift-schadenmeldung.properties at line 85 — already among the sources");
    expect(text).toContain("another code search — not run: over the limit of calls in one round");
    expect(text).toContain("another code search in loom — not run: that repository is outside this turn");
    expect(text).toContain("another code search — not run: it named nothing to look up");
    expect(text).toContain(
      "Named ServiceLinkdata.java line 52 (schadenmeldung-service) as the place to write the answer from.",
    );
    for (const leak of ["FOUND", "grep(", "verschlüsselt", "decrypt"]) expect(text).not.toContain(leak);
  });

  it("reads a stored NOT FOUND sentence as the plain not-found line", () => {
    const text =
      gathered({
        locate_rounds: 1,
        locate_calls: ["grep(Arbeitsunfaehigkeit)"],
        locate_empty: ["grep(Arbeitsunfaehigkeit)"],
        locate_found: "NOT FOUND: the specific place where a bestehende Arbeitsunfähigkeit is corrected was not located.",
      }).container.textContent ?? "";
    expect(text).toContain("Did not find the exact place.");
    expect(text).not.toContain("bestehende");
  });

  it("says why the locate loop stopped", () => {
    strict(
      <Trace
        steps={[
          {
            step: "gathering",
            at: t0,
            detail: { hits: 1, references: 0, crossings: 0, sources: 1, repos: 1, locate: "call failed", locate_rounds: 1 },
          },
        ]}
        state="done"
        startedAt={t0}
        endedAt={t0 + 100}
      />,
    );
    expect(screen.getByText(/call failed/)).toBeTruthy();
  });

  it("draws no detail row for a step that reported none", () => {
    // Every turn stored before the detail existed, and every step that has
    // nothing to say: the label and the duration alone, as before.
    const { container } = strict(
      <Trace steps={[{ step: "searching", at: t0 }]} state="done" startedAt={t0} endedAt={t0 + 100} />,
    );
    expect(container.querySelector(".trace-detail")).toBeNull();
  });
});
