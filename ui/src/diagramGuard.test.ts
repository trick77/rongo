import { describe, it, expect } from "vitest";
import { draw } from "@trick77/diagram/mermaid";
import { diagramSource } from "./diagram";

/**
 * The package's real draw, no stub, on sources that reach the mermaid
 * renderer: past the own drawing's caps, or a kind it does not draw. The
 * renderer is gated (a source that could make the browser fetch is refused
 * and shown as code), and these hold the gate to what rongo's answers
 * write: code words and generics are drawn, a fetch is refused.
 *
 * jsdom cannot lay text out, so a source the gate lets through may still
 * fail to render here; what is asserted is that the refusal is never the
 * gate's ("unsafe: …"), and that a source it must refuse says so.
 */

/** refusal is the gate's reason, or null when the gate let it through. */
async function refusal(src: string): Promise<string | null> {
  const out = await draw(src);
  if (!("error" in out)) return null;
  return out.error.startsWith("unsafe:") ? out.error : null;
}

/** classes past the own drawing's cap (8), so the renderer draws them. */
function manyClasses(extra: string): string {
  const lines = ["classDiagram"];
  for (let i = 0; i < 10; i++) lines.push(`  class C${i}`);
  for (let i = 1; i < 10; i++) lines.push(`  C0 <|-- C${i}`);
  return [...lines, extra].join("\n");
}

/** overCap is a flowchart past the own drawing's cap (16 nodes), so the
 * renderer draws it, ending in one node labelled as given; extra lines
 * (a style, say) follow. */
function overCap(label: string, ...extra: string[]): string {
  const steps = Array.from(
    { length: 18 },
    (_, i) => `  n${i}["Step ${i}"] --> n${i + 1}["Step ${i + 1}"]`,
  );
  return ["flowchart TD", ...steps, `  n18 --> z["${label}"]`, ...extra].join(
    "\n",
  );
}

describe("the renderer's gate", () => {
  it("draws a class diagram naming URLs and generics in its members", async () => {
    const src = manyClasses(
      "  class Resolver {\n    +ResolveURL(raw string) URL\n    -cache Map<String, Object>\n  }",
    );
    expect(await refusal(src)).toBeNull();
  });

  it("draws a sequence with a generic in a message", async () => {
    const actors = Array.from({ length: 10 }, (_, i) => `  participant P${i}`);
    const src = [
      "sequenceDiagram",
      ...actors,
      "  P0->>P9: load List<Order>",
      "  P9-->>P0: Map<String, Object>",
    ].join("\n");
    expect(await refusal(src)).toBeNull();
  });

  it("draws a large stored spec whose labels name a URL and a path", async () => {
    const nodes = Array.from({ length: 20 }, (_, i) => ({
      id: `n${i}`,
      label: i === 0 ? "GET https://api/x" : i === 1 ? "C:\\conf" : `Step ${i}`,
    }));
    const edges = nodes
      .slice(1)
      .map((n, i) => ({ from: nodes[i].id, to: n.id }));
    const body = JSON.stringify({ type: "flow", nodes, edges });
    expect(await refusal(diagramSource("diagram", body)!)).toBeNull();
  });

  it("refuses a source that would fetch, and says why", async () => {
    // A style that loads, on a flow past the own drawing's cap. A directive
    // is no longer the test: its config is inert under the renderer's
    // locked keys, and the package drops it before the gate reads the rest.
    const src = overCap(
      "Done",
      "  style n0 fill:url(https://example.invalid/a.png)",
    );
    expect(await refusal(src)).toMatch(/^unsafe: /);
  });

  // A tag with an attribute is refused in any case, a stray `<` inside it
  // or not (let through by 0.1.9); a bare acting tag in lower case is
  // refused wherever it stands, also straight after a word (`List<script>`,
  // let through by 0.1.8). A bare tag in capitals is text, as is a generic,
  // a stereotype or a comparison with the same brackets: drawn.
  for (const label of [
    "List<script>alert(1)</script>",
    "x<object .a=1 data=/api/logout>",
    "x<img x< onerror=alert(1)>",
    "x<OBJECT x< data=/api/logout>",
  ])
    it(`refuses the label ${label}, and says why`, async () => {
      expect(await refusal(overCap(label))).toMatch(/^unsafe: /);
    });

  for (const label of [
    "Map<String, Object>",
    "List<Template>",
    "a < b and c = d",
    "<<Template>>",
    "Page<Frame>",
    "Step <Script>alert(1)</Script>",
  ])
    it(`draws the label ${label}`, async () => {
      expect(await refusal(overCap(label))).toBeNull();
    });

  it("drops a directive's inert config and draws the rest", async () => {
    const src =
      '%%{init: {"themeCSS": ".x { background: url(https://example.invalid/a.png) }"}}%%\npie title Orders\n  "Web" : 60\n  "Store" : 40';
    expect(await refusal(src)).toBeNull();
  });
});
