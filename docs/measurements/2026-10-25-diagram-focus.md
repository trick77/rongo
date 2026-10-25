# The model marks what the question is about

**Status: measured 2026-10-25 on the flow corpus, BA audience, mimo-v2.6-flash
on both lanes, two arms in ONE database. Every flowchart the arm drew carried
the mark, two nodes each, on the nodes the question was about. Diagrams and
rubric unchanged within judge noise. Shipped, with the browser dropping the
one malformed mark the arm produced.**

## Why

rongo draws a flowchart node tagged `:::focus` in the accent, a 2px blue
frame, and infers nothing: the colour has to mean "what the question is
about", and only the model knows which node that is. Nothing in the prompt
asked for the tag, so no answer ever carried it; the accent existed in the
drawing and never reached a reader.

## What changed

One rule in `answerDiagram`, after the label rule: in a flowchart, mark the
one or two nodes the question is about with `:::focus` right after the node,
and none when the whole picture answers it alike. Flowcharts only: a sequence
has no class syntax, and state and entity diagrams were left out so the arm
measures one thing.

## Numbers

`TestEvalMeasureAnswers`, flow corpus (Sock Shop pinned 2026-09-10, re-indexed
for this measurement, 8 repositories, 2855 chunks), two runs per arm, both arms
against the same database file. Baseline is origin/master `6dcf157`.

| arm | rubric present | contradicted | forbidden | asked | diagrams | flowcharts | marked |
|---|---|---|---|---|---|---|---|
| master, run 1 | 34/39 | 0 | 1 | 1 | 9 / 15 | 1 | 0 |
| master, run 2 | 34/37 | 1 | 1 | 1 | 9 / 15 | 3 | 0 |
| focus line, run 1 | 33/37 | 0 | 2 | 2 | 9 / 15 | 2 | 2 |
| focus line, run 2 | 34/38 | 0 | 2 | 1 | 10 / 15 | 3 | 3 |

What the arm marked:

- Which service decides whether a payment is authorised: the two decisions,
  "Total positive?" and "At or below ceiling?" (both runs, worded apart)
- draw the flow as a diagram: the two gates the order passes
- Wie wird der Rechnungsbetrag berechnet: "Betrag berechnen", plus a stray
  `:::focus` on a line of its own after the last edge

## Reading

- **Every flowchart was marked, always on two nodes, always the ones the
  question names.** The rule's own ceiling held; "mark none" never fired, which
  this corpus cannot tell apart from never applying.
- **The cost columns did not move.** Forbidden went 2 to 4 across two runs.
  All six forbidden claims, both arms, are in answers that drew a sequence
  diagram, which the rule does not touch; three of the four are the
  guest-cart and address questions that also failed in master. That is the one
  or two questions of judge noise every run carries
  (`2026-09-17-ba-diagrams.md`).
- **Diagrams 18 to 19.** A line about diagrams could have cost pictures; it
  cost none against master in the same database.
- **The corpus is mostly sequences.** 14 of 19 diagrams in the arm are
  sequences, so the rule had five flowcharts to act on. The marking rate is
  5/5, but five is five.
- **One malformed mark.** The invoice answer put a bare `:::focus` on its own
  line; the renderer refuses the whole source over it, so the answer would have
  shown "could not be drawn". It went into the corpus first
  (`eval-invoice-focus-de.txt`); the browser now drops a line that is a class
  mark and nothing else before parsing (`dropStrayMarks`, `diagramText.ts`). It
  marks nothing, so dropping it costs nothing a reader sees.

## Also seen, not this change

Both arms fail the test on `failOnWarn`: the gate lane on mimo-v2.6-flash
sometimes answers the reranker as `{"relevant":[[1],[2],…]}`, nested lists,
and the reranker keeps the fused order. Same in both arms, upstream of the
answer prompt, so it does not separate them.
