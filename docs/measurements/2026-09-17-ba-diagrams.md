# Business analyst answers draw the process

**Status: measured 2026-09-17 on the flow corpus, BA audience, MiMo Pro.
Before: 1 diagram in 19 answers. After: 13 in 19, every label under 40
characters, every spec inside the caps, rubric within judge noise. The
prompt changes shipped; `TestEvalMeasureAnswers` now prints the diagram
kind per answer.**

## Why

The diagram rule (`answerDiagram`, `backend/internal/ask/answer.go`) was
appended for every audience, and nothing in the BA prompt barred a picture.
Analyst answers to process questions came back as paragraphs anyway. Three
things in the prompt did that:

1. `answerBA` shaped the whole answer as prose: "three to five paragraphs
   ... Answer the question and then stop." The diagram rule arrived at the
   end of the prompt, after the model had been told to stop.
2. The diagram rule's trigger was a restriction in developer words: "only
   where control flow or a call sequence carries the explanation". A
   business question is about a process, a hand-off, a decision; the words
   never fired.
3. `answerShape` sent "branches, options, ordered steps, conditions" to a
   list, which is the material a flowchart is made of, so the list rule
   won by default.

## What changed

- `answerBA` names when a picture belongs, in the reader's words: a case
  moving through states, an approval, a hand-off with a decision is a
  flowchart; an exchange between systems, roles or people is a sequence
  diagram. It says "a diagram", never the fence, so the shape rule still
  precedes the first fence in the prompt (`answer_test.go`).
- `answerDiagram` opens with a positive, audience-neutral trigger: steps in
  an order with a decision that splits the path, or two or more parties
  exchanging messages, whoever the parties are. Steps with no branch and
  one party stay a list; one rule, one value or one place stays prose. The
  caps and the label rule are unchanged.
- `answerShape` hands ordered steps with a branch or a second party to the
  diagram rule; a plain set stays a list.
- `answerProcesses` asks for the BPMN walk as the flowchart, one node per
  step, the branch condition as the edge label. `answerCompare` and
  `answerCompareProjects` say the diagram is a sequence between the
  repositories or projects where they call each other.
- `ask.DiagramKind` reports "flow", "sequence" or "" for an answer, read
  as the renumberer left it, so a spec fenced as json or written bare
  counts once retagged and an undrawable fence counts as none. The answers
  harness records it and prints `diagram %q` per answer and `diagrams N`
  per run.

## Numbers

Flow corpus (`/tmp/rongo-flow.db`, Sock Shop pinned 2026-09-10), ten
questions, two runs each, BA audience, defaults otherwise. Baseline is
origin/master `cda776c` in its own worktree the same day.

| arm | diagrams | rubric present | contradicted | forbidden | cited parts | asked |
|---|---|---|---|---|---|---|
| master, run 1 | 1 / 9 | 24/26 | 0 | 0 | 15/25 | 1 |
| master, run 2 | 0 / 10 | 28/30 | 0 | 0 | 20/30 | 0 |
| this branch, run 1 | 7 / 10 | 27/30 | 0 | 0 | 19/30 | 0 |
| this branch, run 2 | 6 / 9 | 23/26 | 1 | 1 | 16/25 | 1 |

Which questions drew what, this branch (run 1 / run 2):

- Customer places an order: flow / sequence
- Who is notified when a shipment is due: sequence / sequence
- Guest cart on login: flow / none
- Payment declined: flow / sequence
- Which service authorises a payment, on what basis: none / none
- How a customer gets an account, where the card data lands: none / sequence
- Where the address is stored, who reads it: sequence / sequence
- Which services the order service asks before saving: sequence / asked back
- Shipment when the queue is down: flow / none
- How the invoice amount is computed: none / flow

The one contradicted claim and the one forbidden claim are in the run-2
guest-cart answer, which carries no diagram; the same question was 3/3 in
both master runs and 2/3 with a flowchart in run 1. That is the 1-2 questions
of judge noise every run carries, not a cost of the picture. The
authorisation question, a single rule, drew nothing in either run, which is
what the trigger asks for.

Sizes: 13 specs, flow nodes 6-11, sequence steps 3-10 over 3-5 actors, 96
labels, the longest 36 characters, none over the 40 the rule names.

Tokens per run: 278k / 309k on master, 318k / 286k here. A spec costs a
few hundred output tokens; the spread between runs is larger than that.
