# Intent in the answer prompt, the follow-up rule on a resumed card, the structure block on partial sources

**Status: measured 2026-09-15 on the flow corpus, twice per arm, judge MiMo Pro. The arm holds the baseline on rubric-present (26/28 and 27/30 against 26/30 and 25/30), zero contradictions, and ships. The private estate, where the structure-block failure was seen, is not on this machine; that sentence ships as a prompt rule on the strength of no regression here and is re-read when that corpus is back.**

## Why

Three answer-side gaps, none of them a number: the understanding step's `intent` (how, why, where, conformance) reached only the trace; a follow-up answered through a clarification card lost the "what it follows" rule because the resume paths passed an empty previous question; and the private-estate judge (2026-09-11-answers-judged.md) recorded an answer naming three of seven Maven modules and calling one "not indexed" with all seven listed in the structure block in front of it.

## The change

- `Scope.Intent`, filled from the understanding step, adds one sentence after the shape rule: a WHERE question opens by naming every place the mechanism lives, a WHY question states the rule and the reason the code gives, a conformance question answers yes or no first. HOW adds nothing. Persisted with the scope so a re-explain carries it.
- `Resume` and `ResumeRepo` receive the thread like `Run` does and pass the last answered question before the card into the answer prompt.
- `structureIsConfiguration` gains: when a source shows only some of the parts the block lists, the block is complete and the source is partial; say the parts exist and that their code is not among the sources, never that they are absent or not indexed.

## The arms

`hack/run-flow-eval.sh 'TestEvalMeasureAnswers$'`, product defaults (MiMo 2.5 Pro answers, MiMo 2.5 gates, reranker on), Sock Shop corpus at `pin20260910`, 482 files / 3517 chunks, audience Analyst, two runs each. Baseline is `origin/master` at f38bb58 on the same day.

| arm | run | rubric present | contradicted | forbidden | cited parts | failed upstream | tokens |
|---|---|---|---|---|---|---|---|
| baseline | 1 | 26/30 | 0 | 0 | 19/30 | 0 | 300k |
| baseline | 2 | 25/30 | 0 | 0 | 17/30 | 0 | 303k |
| this branch | 1 | 26/28 | 0 | 1 | 17/28 | 1 | 269k |
| this branch | 2 | 27/30 | 0 | 0 | 18/30 | 0 | 301k |

Per question, the invoice-amount question went 3/3 in the branch's first run (2/3 in every baseline run) and back to 2/3 in the second; the account question stayed 2/3 in both branch runs where the baseline moved 2/3 then 0/3. The forbidden claim: the address question, branch run 1, a confident reading the rubric was written to catch; it did not recur and sits at the corpus's known rate of about one assertion in 140 grades (2026-09-11-answers-judged.md, 2026-09-15-gpt-5-series.md). The upstream failure in run 1 was a stream that ended with no content, the Pro lane's known flake.

## What it says

- No regression on the flow corpus, and the two questions that moved, moved up. The judge noise band is one to two questions, so this is "holds", not "gains".
- The intent and follow-up items are rule fixes, not tuning; they ship on their tests. The structure-block sentence is the one aimed at a measured failure and its corpus is absent; it stays prompt-only and is re-read on the private estate.
- A retried turn in a pinned thread reached `Run` with no pin and no previous question (the retry branch never filled `prior`). Fixed on `feat/answer-retry`.
