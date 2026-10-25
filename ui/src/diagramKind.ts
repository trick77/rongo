/** diagramKind is the first word of the source, which is the diagram type in
 * mermaid's grammar: `flowchart`, `sequenceDiagram`, `stateDiagram-v2`. A
 * leading `%%{init}%%` directive or comment is skipped. Empty when there is
 * no such word, or when the word is not the shape of a type name; the
 * backend reads it the same way (renumber.go mermaidKind).
 *
 * A module of its own so the pure text helpers (diagramText.ts) can read it
 * without loading the component. */
export function diagramKind(src: string): string {
  for (const line of src.split("\n")) {
    const t = line.trim();
    if (t === "" || t.startsWith("%%")) continue;
    const word = t.split(/\s+/)[0].replace(/[;:]$/, "");
    return /^[A-Za-z][A-Za-z0-9-]*$/.test(word) ? word : "";
  }
  return "";
}
