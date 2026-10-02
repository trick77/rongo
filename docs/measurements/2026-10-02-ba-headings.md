# Analyst answers get part headings and short paragraphs

**Status: measured 2026-10-02 on the flow corpus, BA audience, one run per arm,
answer `mimo-v2.6-pro`, gate `mimo-v2.6-flash`, both arms against ONE database.
Headings 0 → 39, paragraphs over three sentences 16 → 2, values in parentheses
5 → 0. Correctness held on the twelve questions both arms answered (30/33 →
29/33, one question); diagrams 7 → 6 on the same twelve. Shipped on the
shape numbers, which no judge re-rolls. Rubric and diagrams are ONE run each
— both gaps are one question, below what this file can conclude. The next
BA measurement reads diagrams against both arms here.**

## Why

Analyst answers read as a wall of text. `answerBA` asked for "three to five
paragraphs" and banned code, signatures and paths, so the model had nothing
to write but dense prose: paragraphs of up to seven sentences, values such as
cron expressions in parentheses mid-sentence. A comparable product's answer of
the same length read easily under two part headings with short paragraphs.

The change, BA only: a paragraph is at most three sentences; an answer of two
or more parts opens each with a short `###` heading in the reader's words, one
part has none; the opening sentence stands above the first heading; an exact
value closes its part as a list item instead of sitting in the prose. Developer
answers are untouched — they have code blocks and paths to break them up.

## The arms

`eval-corpus/flow/run.sh 'TestEvalMeasureAnswers$'` with
`BACKEND_EVAL_ANSWER_RUNS=1`, master `87a6f7c` and this branch, in parallel.
The corpus was rebuilt for this run: 482 files as in
`2026-09-10-flow-corpus.md`, but 2855 chunks against its 3517 — chunking has
changed since. Numbers do not compare with earlier tables.

| arm | answered | rubric present | contradicted | forbidden | cited parts | diagrams | tokens |
|---|---|---|---|---|---|---|---|
| master `87a6f7c` | 14 | 36/39 | 0 | 1 | 24/33 | 9 | 617230 |
| this change | 12 | 29/33 | 0 | 0 | 18/27 | 6 | 567296 |

The branch asked back on three questions, master on one. Routing runs before
the answer prompt and is the same code in both arms; the gate model's rerank
reply fell back on several turns in both (`{"relevant":[[1],[13],…]}`, not the
shape the reranker parses), so the card is gate noise, not this change. On the
twelve questions both answered: rubric 30/33 → 29/33 (shipment-on-queue-outage
lost one point), diagrams 7 → 6 (payment authorisation drew none). Both are
one-question gaps.

Shape, measured on the answer text (`##`/`###` lines, sentences per prose
paragraph, parenthesised values carrying a code span or `*`, `/`, `=`):

| arm | answers with headings | headings | prose paragraphs | sentences/para mean | max | paras > 3 sentences | values in parentheses |
|---|---|---|---|---|---|---|---|
| master | 0 / 14 | 0 | 65 | 2.6 | 7 | 16 | 5 |
| this change | 12 / 12 | 39 | 80 | 1.9 | 4 | 2 | 0 |

## Open

Every answered question drew headings. "An answer of one part has none" never
fired, but every flow-corpus question has a trigger, an outcome and a failure
path. Watch it on a one-part question before tightening the wording. Diagrams
are one run, one question down; a second run decides whether that is noise.
