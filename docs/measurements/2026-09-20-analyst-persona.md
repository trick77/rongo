# The Analyst prompt describes the reader: measured, the diagrams held

**Status: measured 2026-09-20 on the flow corpus, BA audience, MiMo Pro.
`answerBA` opened with a job title and one exclusion; it now says what the
reader decides, what they know, and what to write where the mechanism turns
technical. Diagrams went 8/10 to 10/10, rubric 28/30 to 27/30, nothing
contradicted and nothing forbidden on either arm. Shipped as #216 before the
number existed; this documents it after the fact.**

## Why

`answerBA` opened `Audience: business analyst.` and left what that means to
the model's training. Its one exclusion, `does not read code`, was read
narrowly: the model stopped quoting Go and went on explaining deployments and
data models as common knowledge.

The rewrite is a persona, not a task description. Every clause names a move
the model makes rather than one to avoid, which is the shape
`2026-09-17-ba-diagrams.md` established for this block: the negatively phrased
diagram trigger drew 1 picture in 19 answers, the positive rewrite 13.

Two constraints shaped what could be said. The nouns are load bearing —
`configuration` in the code/config/manual sentence, `systems` in the diagram
trigger whose sequence diagrams run over three to five actors, and a corpus
question names a queue outright — so what lies past the reader is the HOW,
never the WHAT EXISTS. And no clause asks for a system to be renamed into
business words: the sources carry the code's names, and inventing one would
decouple a claim from its citation.

A code review raised the one risk the diff could not settle: the new clause
"where the answer would turn on ... how the systems reach each other ...
state the effect the business sees" sits twelve lines above the measured
trigger "where it is an exchange between systems, roles or people, a sequence
diagram". On a cross-system question the two pull opposite ways, and the
predicted resolution was no picture. That is what this arm tests.

## The arms

`scripts/run-flow-eval.sh 'TestEvalMeasureAnswers$'` on the flow corpus, ten
questions, one run per arm, answer and judge on MiMo Pro, defaults otherwise.
Before is `17cb8bc` in its own worktree the same day; after is `1dd5304`.
Both arms ran against ONE database, built once that morning.

Each arm ran as two invocations over disjoint question files
(`BACKEND_EVAL_FLOW_QUESTIONS`): three cross-system questions first, the
remaining seven after. The per-run totals below are their sum. The split
changes nothing the arm measures — each question is its own pipeline run —
and it exists because the first three were enough to answer the review's
question before paying for the rest.

| arm | diagrams | rubric present | contradicted | forbidden | cited parts | tokens |
|---|---|---|---|---|---|---|
| master `17cb8bc` | 8 / 10 | 28/30 | 0 | 0 | 18/30 | 310409 |
| this change `1dd5304` | 10 / 10 | 27/30 | 0 | 0 | 17/30 | 314767 |

Which questions drew what, master / this change:

- Customer places an order: sequence / flow
- Who is notified when a shipment is due: **none / sequence**
- Guest cart on login: sequence / sequence
- Payment declined: flow / flow
- Which service authorises a payment, on what basis: flow / flow
- How a customer gets an account, where the card data lands: sequence / sequence
- Where the address is stored, who reads it: sequence / sequence
- Which services the order service asks before saving: sequence / sequence
- Shipment when the queue is down: sequence / flow
- How the invoice amount is computed: **none / flow**

The review's finding does not reproduce. Every cross-system question drew a
picture on the new prompt, and the two that drew none on master now do. The
shipment-notification question is the sharpest case: a producer and a consumer
sharing a queue name, which is exactly where the new clause was predicted to
abstract the exchange away, and it gained a sequence diagram instead.

The rubric moved 28 to 27 and citations 18 to 17, one question each. Both are
inside the judge noise this arm carries (`2026-09-06-routing-rerun.md`), and
one run per arm cannot separate a one-question move from a re-roll. What the
table settles is the diagram question, where the move is two questions in the
same direction as the measured trigger. Nothing was contradicted and nothing
forbidden on either arm.

Tokens rose 1.4%, which is the longer block reaching every answer.

## The corpus is not the 2026-09-10 one

The pins were rebuilt from the shas in `2026-09-10-flow-corpus.md` and the
file count reproduces exactly at 482. Chunks do not: 2855 here against the
3517 that document records. The data ceiling in `2026-09-17-data-file-cap.md`
postdates it, and a rule re-applied at boot retires what an older build
embedded.

So the 13/19 in `2026-09-17-ba-diagrams.md` is NOT comparable to the numbers
above, and nothing here is read against it. The same-day baseline in the same
database is the comparison, which is what the rule asks for anyway.

## What changed

`answerBA`'s opening two sentences became five, and `followupsPrompt` stopped
naming the reader with a bare job title. Every measured sentence in the block
is byte-identical: the diagram trigger, the lead-sentence restatement, the
code/config/manual classification, the closer and the values paragraph.

`answer_test.go` pins the new wording for BA and its absence for DEV, folding
newlines before matching so the assertion is about the words and not where the
constant wraps. `followups_test.go` does the same for the suggestion call.
