# Lanes and class diagrams in answers

**Status: measured 2026-11-22 on a fixed context, MiMo flash, analyst
audience, two runs per arm. Class questions drew a class diagram 4 of 4
(base 0 of 4), lane questions drew lanes 2 of 4 (base 0 of 4), the misses
still valid diagrams. Rubric 37/38 against base 35/38, the control stayed an
entity diagram. Shipped. Reduced plan for cost: $0.022 for both arms.**

## Why

`@trick77/diagram` draws two shapes rongo's prompt never asked for: a
swimlane (a `flowchart LR` with one subgraph per role, every step in one) and
a class diagram. Both fit questions the flow corpus already has the code
for: who does which step as an order or a registration passes between
services, and how a service's types are built (an interface, its
implementation, the decorators around it).

## What changed

- `answerDiagram` (`backend/internal/ask/answer.go`): two rows in the type
  table. Lanes are the branch row done by several roles, worded as the
  package reads them (every step inside a role, arrows within a role too,
  at most 5 roles; two roles with every arrow from one into the other are
  the mapping). The class row names the syntax the package reads (members
  bare, `List~Item~`, `<<interface>>`, the arrows) and its caps (8 classes,
  12 relations, 10 members); stored records stay the entity diagram.
- `answerBA` names lanes in the reader's words ("who does which step, as the
  work passes between roles"); `answerDev` names the class diagram. In the
  audience blocks because a rule in `answerDiagram` alone never fired
  (`2026-09-17-ba-diagrams.md`).
- `ask.DiagramShape` tallies a swimlane apart from a flowchart, by the
  package's rule, held to the browser's reading on the corpus
  (`eval-swimlane-checkout-en`, `mapping-lr`, `eval-class-payment-en`; the
  two `eval-*` files are run 1's answers below).
- Questions: `backend/internal/retrieve/eval/diagram-questions.json` and
  `diagram-rubrics.json`, Sock Shop at the flow corpus's pins: two lane
  questions (checkout, registration; en, de), two class questions (payment's
  and user's `Service` with their wrappers; en, de) and one control, the
  order's stored types, which should stay an entity diagram.

## Arms

Reduced for cost (hard cap $1): no index, no embedding call, no flow corpus
run. `TestEvalMeasureDiagramPrompt` answers the five questions from a FIXED
context, the excerpts frozen per question in `diagram-context.json`, through
the product's Answerer: the only thing that differs between the arms is the
system prompt. Judged against the rubrics as `TestEvalMeasureAnswers`
judges. MiMo flash on both lanes and as judge, analyst audience (the
default), each arm twice:

| arm | checkout |
|---|---|
| base | the branch's base commit `f02dc79`, with the harness and its three JSON files copied in |
| branch | this branch |

The base harness records the fence's first word; both answer files are
tallied with `TestEvalTallyDiagramShapes`. Each arm stops before the next
call once its spend reaches `BACKEND_EVAL_COST_CAP_USD` or its tokens reach
`BACKEND_EVAL_TOKEN_CAP`; `SPENT arm …` in the log is the total.

Not shown by the reduced plan: whether the flow corpus's existing diagrams
shift (retrieval-dependent answers), and the developer audience.

## Numbers

| arm | run | rubric | contradicted | forbidden | diagrams | shapes |
|---|---|---|---|---|---|---|
| base | 1 | 18/19 | 0 | 0 | 5/5 | sequence 4, entity 1 |
| base | 2 | 17/19 | 0 | 0 | 4/5 | sequence 3, entity 1, none 1 |
| branch | 1 | 19/19 | 0 | 0 | 5/5 | swimlane 2, class 2, entity 1 |
| branch | 2 | 18/19 | 0 | 0 | 5/5 | class 2, entity 1, flowchart 1, sequence 1 |

Per question (run 1 / run 2):

| question | base | branch |
|---|---|---|
| checkout, who does which step (lanes) | sequence / sequence | swimlane / flowchart |
| registration, who does which step (lanes, de) | sequence / sequence | swimlane / sequence |
| payment `Service` and its wrapper (class) | sequence / none | class / class |
| user `Service` and its wrappers (class, de) | sequence / sequence | class / class |
| an order's stored types (control) | entity / entity | entity / entity |

Every fence of both arms parses, is drawn by the package (none handed to
the renderer) and passes `checkIR` (package 0.1.3).

Spend, as llmwire priced it: base 41,939 tokens in, 19,711 out, $0.0092;
branch 45,551 in, 31,569 out, $0.0128; $0.022 together. The branch writes
about 60% more output: a class diagram lists members.

## Reading

- **The class row works.** 4 of 4 class questions drew a class diagram,
  where the base drew a sequence of calls through the wrappers or nothing.
- **Lanes fire half the time.** 2 of 4, both in run 1; run 2 drew a
  flowchart for checkout and a sequence for registration. Both misses are
  valid pictures the package draws; nothing broke. No more prompt here
  without a number: a second wording is a new arm.
- **No rubric cost.** 37/38 against 35/38, inside the one-or-two-claim
  judge noise every run carries; nothing contradicted or forbidden in
  either arm.
- **The control holds.** The order's stored types stayed an entity diagram
  in all four answers: the class row does not claim the entity row's
  material.
