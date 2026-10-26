# claude-opus-5-5 on both lanes

**Status: measured 2026-10-26 on the flow corpus, BA audience, two runs per
arm, both arms against ONE database. Opus on both lanes matched or beat the
mimo-v2.6-flash baseline on rubric present and cited parts, contradicted
nothing, answered every question, and finished in a third of the wall-clock
time; it asserted one forbidden claim in run 1 where mimo asserted none.
Shipped as the example lane model in `.env.example`.**

## Why

llmwire learned Anthropic's Messages protocol (trick77/llmwire#74), so
`claude-opus-5-5` became a profile id rongo can name like any other. A model
change is judged by `TestEvalMeasureAnswers`, run twice (AGENTS.md).

## Setup

- Corpus: Sock Shop pinned 2026-09-10 (`2026-09-10-flow-corpus.md`),
  re-indexed for this measurement: 8 repositories, 2855 chunks,
  text-embedding-3-small.
- llmwire `v0.1.9-0.20261026072811-61ed84c8533a`.
- Baseline arm: `BACKEND_LLM_MODEL` and `_GATE_MODEL` mimo-v2.6-flash.
- Candidate arm: the same, plus `BACKEND_EVAL_ANSWER_MODEL` and
  `BACKEND_EVAL_GATE_MODEL` claude-opus-5-5. The judge stays on the product
  models (mimo-v2.6-flash) in both arms, so the grader did not change with the
  candidate.
- Opus runs unpinned: no `BACKEND_LLM_REASONING`, so its default effort
  (medium); the gate lane at ReasoningMinimal, which is effort low on Opus
  (it cannot switch thinking off).

## Numbers

`scripts/measure.sh flow 'TestEvalMeasureAnswers$'`, both arms run at once.

| arm | rubric present | contradicted | forbidden | cited parts | asked | failed | tokens |
|---|---|---|---|---|---|---|---|
| mimo-v2.6-flash, run 1 | 37/41 | 1 | 0 | 22/35 | 0 | 0 | 655014 |
| mimo-v2.6-flash, run 2 | 31/36 | 1 | 0 | 23/32 | 1 | 1 | 554758 |
| claude-opus-5-5, run 1 | 37/41 | 0 | 1 | 24/35 | 0 | 0 | 988619 |
| claude-opus-5-5, run 2 | 38/41 | 0 | 0 | 24/35 | 0 | 0 | 988986 |

Wall-clock for both runs: mimo-v2.6-flash 3599s, claude-opus-5-5 1259s.

## Reading

- **Opus is at least as right, and steadier.** 37 and 38 of 41 rubric claims
  against 37/41 and 31/36; mimo's second run asked once and failed one
  question outright, so its denominator shrank. Opus contradicted nothing in
  either run; mimo contradicted one claim in each.
- **One forbidden claim on Opus run 1**, none on run 2: within the noise the
  measurement documents have recorded for this corpus (one per arm is
  common), not a pattern.
- **Faster, not slower.** A third of mimo's wall-clock, with the same
  questions, retrieval and judge. The 2026-09-23 lane choice was made on
  speed; Opus does not lose it.
- **No call ran out of room.** Opus cannot switch thinking off, so even the
  gate calls (title 48, route 128, understand 512, locate 700 answer
  tokens) think at effort low inside the answer budget plus llmwire's
  unmeasured 1024-token allowance. No call in either run ended on
  `length`. A prompt that thinks harder than these did would; a measured
  `reasoning.overhead` in llmwire's profile is the fix if one shows up.
- **The judge is the product model.** `judgeLLM` reads
  `BACKEND_LLM_MODEL`, and every number on record was graded by
  mimo-v2.6-flash. `.env.example` now names Opus, so a measurement started
  from it would switch the grader with the example: keep
  `BACKEND_LLM_MODEL` and `_GATE_MODEL` at mimo-v2.6-flash in the eval
  `.env` and put a candidate in `BACKEND_EVAL_*`, as this one did.
- **The answer eval does not run the locate loop.** `TestEvalMeasureAnswers`
  builds its gatherer without `WithLocateLoop`; the product turns it on
  (`BACKEND_LOCATE_ROUNDS`, 3). The loop now replays the model's own turn
  (`ToolTurn.Assistant`) so Opus keeps its signed thinking across rounds
  instead of re-thinking each one. Checked live with `TestLocateAnswer` on
  this corpus ("Where is the shipping cost added to the order total?",
  needle 4.99 in `OrdersController.java`): loop off, 1 round and 3 rounds
  all ran on Opus with the replay and no wire error. None found the
  constant: the loop's own greps inside orders were refused by the scope
  narrowing, which predates this change and is the same on any model.
- **Dearer per token.** About 1M tokens a run against 0.55-0.66M, at list
  rate $4 / $20 per million (llmwire's profile prices each call). A
  deployment that names Opus pays that; mimo-v2.6-flash stays a profile id
  away, with its own key.
- One question missed in every run of BOTH arms: "In that flow, where is the
  shipping cost added?", 0/1. The rubric wants the orders service's
  hard-coded 4.99 added to the order total; neither model found it (mimo
  contradicted it once). A retrieval gap, not a model one: the follow-up is
  pinned to the shipping repository (rung named_repos), and the constant
  lives in orders.
