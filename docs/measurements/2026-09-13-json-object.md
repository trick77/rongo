# response_format on the gate calls: nothing to fix, and it stays off

**Status: measured 2026-09-13 on the flow corpus (10 questions, twice per
arm). Asking the endpoint for a JSON object on the five parsed calls moves
the rubric score inside judge noise and fixes no decode failure, because
there were none to fix. The option ships defined and off.**

## Why it was tried

Every call whose whole reply is parsed — understand, the routing judge, the
choosable gate, naming, the reranker — asks for JSON in its prompt and then
salvages the reply through `llmwire.JSONObject`, which cuts the first
balanced object out of a fence or a sentence. llmwire's README quotes one
model on which a prompt asking for JSON parsed 0 times out of 8 and
`response_format: json_object` 8 out of 8. Both MiMo profiles declare the
format honoured. The question was whether rongo was paying for that salvage
in lost turns.

## The arm

`llm.WithJSONObject()` sets `response_format: {"type": "json_object"}` on a
call; the arm added it to the five call sites above and to nothing else. The
answer call, title and follow-ups write prose and were untouched. The eval
judge was untouched, being the instrument.

## The numbers

`scripts/run-flow-eval.sh 'TestEvalMeasureAnswers$'`, corpus rebuilt at the
pinned shas (482 files, 3517 chunks, identical to
`2026-09-10-flow-corpus.md`), llmwire v0.0.18, Analyst audience:

| arm | run 1 | run 2 | cited parts |
|---|---|---|---|
| baseline (prompt only) | 28/30 | 26/30 | 19/30, 19/30 |
| `json_object` | 25/27 | 26/30 | 18/27, 18/30 |

Run 1 of the arm lost one question to the Pro lane: the answer call returned
115 completion tokens and no text. That call does not carry the format, and
the same question scored 3/3 in run 2; it is the upstream flake
`2026-09-11-answers-judged.md` records, not the arm.

Decode failures across the 20 baseline turns, every parsed call counted:
**zero**. The salvage never fired. There was no loss for the format to
recover, and the difference between the arms is the one-to-two-question
re-roll every pair of runs shows.

One question in the arm's second run, *Welche anderen Dienste fragt der
Bestelldienst ab*, fell from 4/4 to 2/4 with the same route decision and a
different reranked list. One question, one run: not read, per the rule.

## What ships

`WithJSONObject` stays in `internal/llm` with its test and no caller, the way
`RouteSuffix` and `WholeFileTokens` stay: the knob exists so the next person
finds this table before spending forty minutes and the endpoint's money on
the same idea. Switching it on needs a corpus with decode failures in the
baseline, and there is none today.
