# Diagram labels: angle brackets kept by escaping, not by prompt

**Status: a prompt line barring angle brackets was measured 2026-10-25 on the
flow corpus and NOT shipped. The renderer path escapes them instead
(`escapeAngles`, `ui/src/diagramText.ts`).**

## Why

mermaid's sanitiser takes anything in angle brackets for a tag and drops it,
in a node label and an edge label alike, before rongo reads the parse. An
answer mapping webfetch's failures to their messages drew `Failed to fetch
<url>: <cause>` as `Failed to fetch :`; a threshold `retries < 3` loses its
tail the same way. rongo's own `plain()` (`ui/src/diagramIR.ts`) strips
tag-shaped text too, so a sanitiser that stopped doing it would not bring the
words back on its own.

## Measured, then dropped

A line in `answerDiagram` asked for placeholders as plain words. Flow corpus
rebuilt the same day (the eight Sock Shop repositories pinned at the shas of
`2026-09-10-flow-corpus.md`, 2,855 chunks), BA audience, mimo-v2.6-flash on
both lanes, one `TestEvalMeasureAnswers` invocation per arm (two runs):

| arm | diagrams | rubric present | contradicted | forbidden | asked | brackets in a diagram |
|---|---|---|---|---|---|---|
| master `4574d5c`, run 1 | 10 | 29/33 | 1 | 1 | 2 | 0 |
| master, run 2 | 11 | 36/39 | 0 | 1 | 1 | 0 |
| prompt line, run 1 | 10 | 37/39 | 0 | 2 | 1 | 0 |
| prompt line, run 2 | 10 | 34/37 | 1 | 1 | 2 | 0 |

No harm, and no evidence of good: none of the 41 fences used brackets in
either arm. It was dropped for what a prompt cannot do: it fixes no stored
answer (a thread is a record), only lowers the odds for new ones, and as
worded it barred the `-->` arrows, `<br>` and comparisons along with the
placeholders. Escaping on the way to the renderer keeps every stored and new
diagram's text, on both drawing paths, and leaves the prompt byte-identical.
`corpus.test.ts` now fails if a label's `<` or `>` does not reach the drawing
(`eval-webfetch-errors-en`).
