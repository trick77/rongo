# The model marks what the question is about

**Status: measured 2026-10-25 on the flow corpus, BA audience, mimo-v2.6-flash
on both lanes, three arms in ONE database. Every flowchart either wording drew
carried the mark, on the nodes the question was about. Diagrams and rubric
unchanged within judge noise. Both wordings wrote one stray mark on a line of
its own, on the same question; the browser drops it. Shipped.**

## Why

rongo draws a flowchart node tagged `:::focus` in the accent, a 2px blue
frame, and infers nothing: the colour has to mean "what the question is
about", and only the model knows which node that is. Nothing in the prompt
asked for the tag, so no answer ever carried it; the accent existed in the
drawing and never reached a reader.

## What changed

One rule in `answerDiagram`, after the label rule: in a flowchart, mark the
one or two nodes the question is about, the mark right after the node's
shape and never on a line of its own; none when the whole picture answers it
alike. Flowcharts only: a sequence has no class syntax, and state and entity
diagrams were left out so the arm measures one thing.

The first wording wrapped so that a prompt line opened on `:::focus`, and the
review read the stray mark below as the model copying that layout. The second
wording keeps the mark attached to its example and says "never on a line of
its own"; `answer_test.go` refuses a prompt line opening on a class mark. It
did not stop the stray mark (see below), so the layout was not the cause, or
not the only one.

## Numbers

`TestEvalMeasureAnswers`, flow corpus (Sock Shop pinned 2026-09-10, re-indexed
for this measurement, 8 repositories, 2855 chunks), two runs per arm, all three
arms against the same database file. Baseline is origin/master `6dcf157`.

| arm | rubric present | contradicted | forbidden | asked | diagrams | flowcharts | marked | stray mark |
|---|---|---|---|---|---|---|---|---|
| master, run 1 | 34/39 | 0 | 1 | 1 | 9 / 15 | 1 | 0 | 0 |
| master, run 2 | 34/37 | 1 | 1 | 1 | 9 / 15 | 3 | 0 | 0 |
| first wording, run 1 | 33/37 | 0 | 2 | 2 | 9 / 15 | 2 | 2 | 0 |
| first wording, run 2 | 34/38 | 0 | 2 | 1 | 10 / 15 | 3 | 3 | 1 |
| shipped wording, run 1 | 31/34 | 0 | 1 | 3 | 9 / 15 | 3 | 3 | 1 |
| shipped wording, run 2 | 30/34 | 2 | 0 | 2 | 10 / 15 | 1 | 1 | 0 |

What the shipped wording marked:

- Which service decides whether a payment is authorised: "At or under
  threshold?"
- Wie wird der Rechnungsbetrag berechnet: "Betrag berechnen" and "Zahlung
  erlaubt?", plus a stray `:::focus` after the last edge
- draw the flow as a diagram: "Payment authorised?"
- What happens to a shipment when the queue is unavailable: "Queue reachable?"
  and "Failure logged, message dropped"

## Reading

- **Every flowchart was marked, 9 of 9 across both wordings, one or two nodes,
  always the ones the question names.** "Mark none" never fired, which this
  corpus cannot tell apart from never applying.
- **The cost columns did not move.** Rubric 68/76 in master, 67/75 and 61/68
  with the line: 89 to 90 per cent in all three. Contradicted and forbidden
  move by one or two per arm in both directions, and every one of them, all
  three arms, is in an answer drawing a sequence diagram or none, which the
  rule does not touch. That is the one or two questions of judge noise every
  run carries (`2026-09-17-ba-diagrams.md`).
- **Diagrams 18, 19, 19.** A line about diagrams could have cost pictures; it
  cost none against master in the same database.
- **The corpus is mostly sequences.** The rule had nine flowcharts to act on
  in four runs. Nine is nine.
- **The stray mark is the model's, not the layout's.** Both wordings wrote a
  bare `:::focus` on its own line after the last edge, both times on the invoice
  question. The renderer refuses the whole source over it, so that answer would
  show "could not be drawn". Both are in the corpus
  (`eval-invoice-focus-de.txt`, `eval-invoice-focus2-de.txt`); the browser drops
  a line that is a class mark and nothing else before drawing and before Copy
  as Markdown (`dropStrayMarks`, `diagramText.ts`), flowcharts only. It marks
  nothing, so dropping it costs nothing a reader sees; it is never attached to
  the node above, which in both cases was the last edge's target, not the
  node the question was about.

## Also seen, not this change

Every arm fails the test on `failOnWarn`: the gate lane on mimo-v2.6-flash
sometimes answers the reranker as `{"relevant":[[1],[2],…]}`, nested lists,
and the reranker keeps the fused order. Same in all arms, upstream of the
answer prompt, so it does not separate them.
