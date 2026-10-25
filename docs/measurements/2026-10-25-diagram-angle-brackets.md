# Diagram labels: no angle brackets

**Status: measured 2026-10-25 on the flow corpus, BA audience,
mimo-v2.6-flash on both lanes. No harm: diagrams 10 + 11 before, 10 + 10
after; rubric within judge noise. The prompt line shipped.**

## Why

The renderer's sanitiser takes anything in angle brackets for a tag and drops
it, in a node label and an edge label alike. An answer mapping webfetch's
failures to their messages drew `Failed to fetch <url>: <cause>` as `Failed
to fetch : `. mermaid's own drawing did the same; it is not the editorial
renderer's doing, and the text is gone before rongo reads the parse.

## What changed

`answerDiagram` (`backend/internal/ask/answer.go`): no angle brackets in the
diagram, a placeholder written as a plain word.

## Numbers

Flow corpus rebuilt the same day: the eight Sock Shop repositories cloned and
pinned at the shas of `2026-09-10-flow-corpus.md`, indexed into one database
(2,855 chunks). Both arms against that database, one `TestEvalMeasureAnswers`
invocation each (two runs).

| arm | diagrams | rubric present | contradicted | forbidden | asked | brackets in a diagram |
|---|---|---|---|---|---|---|
| master `4574d5c`, run 1 | 10 | 29/33 | 1 | 1 | 2 | 0 |
| master, run 2 | 11 | 36/39 | 0 | 1 | 1 | 0 |
| no brackets, run 1 | 10 | 37/39 | 0 | 2 | 1 | 0 |
| no brackets, run 2 | 10 | 34/37 | 1 | 1 | 2 | 0 |

No diagram on this corpus used angle brackets in either arm, 41 fences in all,
so the corpus cannot show the line working; it shows it costs nothing. The
case that does is `eval-webfetch-errors-en` in the diagram corpus. Master's
run 1 also had one setup turn ask back, which fails the test on master as on
any arm.
