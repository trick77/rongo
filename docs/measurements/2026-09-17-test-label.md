# Test sources are labelled in the answer and rerank prompts; tests are not excluded

**Status: measured 2026-09-17 on the flow corpus (Sock Shop at pin20260910, rebuilt: 482 files, 3516 chunks), `TestEvalMeasureAnswers` twice per arm against a same-day `origin/master` baseline, plus `TestFlowGathered` on both. The label ships. Excluding test files from the index was considered and rejected.**

## Why

A production thread cited `registry_test.go` for "profiles declaring chat capabilities are rejected" where the rejection sits in `registry.go` beside it. The test was a correct hit and a wrong citation. Retrieval already demotes test hits (`retrieve.DefaultTestDecay = 0.35`), the walk orders them last in a hop (`ask.mechanismFirst`) and the card leaves test-only modules out (`ask.onlySupporting`), but nothing after fusion says what a hit is: `renderSources` and the reranker header print repository, path, line and symbol, so the answer model reads a fake as the client it fakes, and the reranker reads a pool of bare excerpts in which "verify that X calls Y" is the clearest description of X.

The alternative on the table was excluding test classes and specs from the index. The same production thread's third turn asked how profiles are added *and tested*, and its two sections on live probes and unit tests have no source but the tests and `ONBOARDING.md`. That question is real, and "how is this tested" is one an analyst asks. A drop is unrecoverable where a demotion costs rank, which is the reason the doc demotion is a decay and not a filter (`2026-09-05-doc-demotion.md`); the same reasoning applies here, and `BACKEND_INDEX_EXCLUDE` exists for a deployment that wants tests out.

## The change

Both headers carry ` (test)` after the symbol when `retrieve.IsTestPath` says so, the same predicate the decay and the walk use. The answer prompt gets one rule beside the documentation rules: a test shows what the code is expected to do, not the code that does it; a claim about the mechanism cites the source holding the mechanism, a test beside it at most as a second marker; a claim resting only on a test says so; a question about testing is answered from tests. The rerank system prompt gets one sentence: prefer the code a test exercises unless the question asks how something is tested.

No change to `DefaultTestDecay`. 0.35 landed with PR 51 without a sweep, and a sweep was not run here: the defect seen is citation choice, not rank, and the corpus has no test-led question, so a sweep could only show cost.

## The arms

`TestEvalMeasureAnswers`, audience BA, ten questions with thirty rubric claims, reranker on, gap pass off, two runs per arm on one database. The judge re-rolls one to two claims per run, so the pairs are read together.

| arm | run | rubric present | contradicted | forbidden | cited parts | tokens | citations | of which tests | of which docs |
|---|---|---|---|---|---|---|---|---|---|
| origin/master (9bbd9d8) | 1 | 27/30 | 0 | 0 | 18/30 | 310133 | 65 | 4 | 0 |
| origin/master (9bbd9d8) | 2 | 27/30 | 0 | 0 | 16/30 | 311323 | 74 | 1 | 0 |
| test label | 1 | 28/30 | 0 | 0 | 18/30 | 307820 | 85 | 2 | 1 |
| test label | 2 | 28/30 | 0 | 0 | 19/30 | 307675 | 81 | 1 | 0 |

The baseline's run 2 has one answer with no citation marker at all (the queue-unavailable question), which is where its 16 comes from; the arm has none.

How the remaining test citations read, which is the thing the rule is about:

| arm | question | test cited | the sentence carrying it |
|---|---|---|---|
| baseline, run 1 | Zahlung abgelehnt | `payment/service_test.go` three times | "Ein Betrag von null oder negativen Werten führt direkt zu einer Ablehnung [3][4][5]" and the threshold sentence: the test presented as the rule |
| baseline, run 1 | queue unavailable | `ITShippingController.java` | "the system logs an error ... but proceeds as if the shipment was successfully accepted [1][2]": the test as the mechanism |
| baseline, run 2 | which service authorises | `payment/component_test.go` | a bare "[3][4][5]" line: the test as the rule |
| label, run 1 | customer places an order | `payment/component_test.go` | "[6][7] If the amount is zero, negative, or exceeds the limit, the payment is declined": the test as a second marker after the code |
| label, run 1 | queue unavailable | `ITShippingController.java` | "A test confirms this exact scenario: it forces the queue send to throw ... [2]" |
| label, run 2 | queue unavailable | `ITShippingController.java` | "A unit test confirms this behaviour explicitly: ... [2]" |

`TestFlowGathered`, product arm (short-gate rerank over 60, 800-rune excerpts, symbol walk, crossings): 28/30 parts and 8/10 questions whole on both sides; mean sources 168.6 on the baseline, 172.9 with the label. The reranker tag costs nothing that this corpus can see.

## What it says

- **The label moves the citation, not the recall.** Five test citations across two baseline runs, all presented as the mechanism; three with the label, two of them named as a test in the sentence and the third a second marker behind the code. Rubric 27, 27 → 28, 28 is inside the judge's band and is not the claim; the sentences are.
- **Tests stay in.** The thread that prompted this needed them, and the rule says when they are the answer.
- **Not done here:** a `TestDecay` sweep (no test-led question to read a benefit from), and whether a diagram node should drop test markers the way `docMask` drops doc markers. The second is a product decision: it would take the chips off a "how is this tested" diagram.
