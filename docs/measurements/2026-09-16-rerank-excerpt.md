# The reranker reads 800 runes per candidate: two more questions gathered, pool stays at 60

**Status: measured 2026-09-15 on the pinned Go corpus and the flow corpus, twice per arm, judge and reranker on MiMo 2.5. 800 runes at pool 60 gathers 41 of 42 unique questions in both runs against the same-day product's 39 and 39, and ships as the default. A pool of 100 buys one question at five and loses one gathered; it stays at 60.**

## Why

`retrieve.LLMReranker` handed the gate model the header and the first 240 BYTES of each candidate, cut inside a UTF-8 rune and mid-line, with no line number. 2026-09-11-arms-after-the-crossing.md left two unique misses the reranker could not lift (fused rank 49, and one absent from the top 200) and one at rank 25. A signature and its doc comment are often longer than 240 bytes; the model was ranking on a truncated first line.

## The change

- The excerpt is counted in runes and cut back to the last line break in the second half of the window, so the model reads whole lines; a single overlong line is cut at the width.
- The header carries the chunk's start line (`[n] repo path:line (symbol)`), so two chunks of one file are distinguishable.
- The reply cap grows with the number of results asked for (`max(256, 64 + 4 * k)`), so a longer list cannot end in `finish_reason=length`.
- `Excerpt` and the pool are harness knobs (`BACKEND_EVAL_RERANK_EXCERPT`, `BACKEND_EVAL_RERANK_POOL`); the product default moves to 800.

The raw question stays the only query text the reranker sees.

## The arms

Go corpus pinned 2026-08-20 (peeq bb04021 526/5694, rongo 0b989b6 142/1358, go-sqlite3 3fa3f30 293/2180), `scripts/run-eval.sh 'TestEvalMeasureRerank$'`, unique cohort n=42. The baseline is `origin/master` at f38bb58 run the same evening, not the 2026-09-11 table, because this branch changes the prompt the 240 arm produces (the line-break cut and the start line are unconditional). Two runs each.

| arm | run | r@5 | r@20 | MRR | gathered | ambiguous | composition |
|---|---|---|---|---|---|---|---|
| product, 240 bytes, pool 60 | 1 | 0.857 (36) | 0.929 (39) | 0.727 | 0.929 (39) | 15/16 | 5/5 |
| product, 240 bytes, pool 60 | 2 | 0.857 (36) | 0.929 (39) | 0.716 | 0.929 (39) | 15/16 | 5/5 |
| branch, 240 runes, pool 60 | 1 | 0.833 (35) | 0.929 (39) | 0.722 | 0.929 (39) | 15/16 | 5/5 |
| branch, 240 runes, pool 60 | 2 | 0.786 (33) | 0.929 (39) | 0.684 | 0.929 (39) | 15/16 | 5/5 |
| **branch, 800 runes, pool 60** | 1 | **0.905 (38)** | **0.952 (40)** | **0.827** | **0.976 (41)** | 15/16 | 5/5 |
| **branch, 800 runes, pool 60** | 2 | **0.881 (37)** | **0.952 (40)** | **0.807** | **0.976 (41)** | 15/16 | 5/5 |
| branch, 800 runes, pool 100 | 1 | 0.929 (39) | 0.952 (40) | 0.821 | 0.952 (40) | 15/16 | 5/5 |
| branch, 800 runes, pool 100 | 2 | 0.905 (38) | 0.952 (40) | 0.818 | 0.952 (40) | 15/16 | 5/5 |

Per question: the product misses the heavily-used helper, the follow-up options (fused rank 25) and how-far-from-a-hit (fused rank 49) in both runs. At 800 runes the follow-up options question is gathered in both runs and nothing is lost; the other two stay missed, the rank-49 one as the reranker's documented limit. The branch's own 240-rune arm is below the product's: cutting back to a line break at 240 leaves as little as 120 runes, so the width and the cut ship together, never the cut alone.

Flow corpus (Sock Shop at pin20260910, 482 files / 3517 chunks), `scripts/run-flow-eval.sh TestFlowGathered`, the product arm:

| arm | parts | questions whole | mean sources |
|---|---|---|---|
| product, same evening | 28/30 | 8/10 | 167.0 |
| 800 runes, pool 60, run 1 | 27/30 | 7/10 | 174.2 |
| 800 runes, pool 60, run 2 | 27/30 | 7/10 | 175.1 |
| 800 runes, pool 100, run 1 | 27/30 | 7/10 | 170.1 |
| 800 runes, pool 100, run 2 | 28/30 | 8/10 | 169.9 |

The one-part moves are inside this corpus's re-index and tie-order band (2026-09-13-infra-stages.md: 25 to 28 on the same code); the part that moves is a crossing landing on the account question, which the wider hits push past the walk's budget in some runs. Not read as a loss, and not as a gain.

## What it says

- **Width is the lever, not depth.** The model was ranking on a truncated first line; 800 runes shows it the signature, the doc comment and the first statements. A pool of 100 hands it forty more candidates and gains one at five while a candidate it would have gathered at 60 drops out.
- **Two gathered questions in both runs is above the noise floor** (AGENTS.md: never conclude from a one-question gap). MRR moves from 0.72 to 0.81 and 0.83.
- **Cost**: gate input roughly triples per search (60 x ~250 tokens instead of 60 x ~70); at the gate lane's list price that is well under a cent per turn.
- Not measured: `TestEvalMeasureAnswers` with the wider excerpt; the change is in what is gathered, and the gathered set is what that judge reads.
