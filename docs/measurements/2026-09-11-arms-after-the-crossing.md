# Three arms after the crossing: where the misses sit, a reranker, and reading the whole file

**Status: measured 2026-09-11 on the pinned Go corpus (65 questions) and the
flow corpus (10). A short-gate reranker over a pool of sixty lifts unique
gathered recall from 0.905 to 0.952, composition from 4/5 to 5/5 and the flow
corpus from 27/30 to 28/30, twice over, and ships. Whole-file reading buys
nothing on the Go corpus and costs two parts on the flow corpus, and stays
off.**

## Where the unique misses sit

`2026-09-06-composition-parts.md` closed retrieval on the Go corpus: 38 of 42
unique questions gathered, grounding 1.000 on every answered one. The four
that miss were the only room left, and the question a reranker turns on is
whether their files are in the candidate pool at all. `TestEvalRankOfMisses`
searches the expanded question with a pool and a cut of 200 and reports every
unique question outside the top 20:

| question | fused rank |
|---|---|
| How does an Apple TV get at the media file without signing in? | 37 |
| How is a channel filter kept from taking effect only after the neighbour search? | 21 |
| From when on does a video count as old material? | 22 |
| Why is a heavily used helper not in every answer? | not in the top 200 |
| How many options may a follow-up question offer at most? | 25 |
| How far from a search hit may code sit and still make it into an answer? | 49 |
| What has to be installed on the machine before rongo's development setup runs? | 21 |

Six of seven sit between 21 and 49: found, and ranked just past the cut. Two
of them the symbol walk already rescues. That is the August finding again,
under the expansion this time, and it says reordering has something to
reorder.

## The reranker arm

`retrieve.LLMReranker`: the fused list is taken to a pool of 60, one
short-gate call reads the question against each result's header and opening
lines and returns the numbers it finds relevant, most relevant first; those
go to the front in the model's order, the rest keep the fused order, and the
list is cut to 20. Nothing is stored — per turn, never indexed, never
embedded — which is what keeps it outside "never store model-written text
about code". A reply that cannot be read leaves the fused order as it was.

`TestEvalMeasureRerank`, unique cohort (42), expanded question, both arms
through the product's gatherer:

| arm | recall@5 | recall@20 | MRR | gathered |
|---|---|---|---|---|
| fused order (the product) | 0.762 (32) | 0.857 (36) | 0.605 | 0.905 (38) |
| **fused order + short-gate rerank over 60** | **0.881 (37)** | **0.929 (39)** | **0.743** | **0.952 (40)** |

Run twice, identical to the question both times. The other cohorts, so a
reorder that lifts the unique misses is not read as a win while it drops
something else out of the cut: ambiguous questions with both alternatives
gathered stay at 15/16, and composition goes from 4/5 to **5/5** — the one
part no arm since August had found, peeq's `store.go` for the foreign-key
question, is in the cut once the model reads the question against the pool.

On the flow corpus the same arm on top of the crossing: **28/30 parts and
8/10 questions whole** against 27/30 and 7/10, at 168.5 mean sources.

**Ships.** The product's retriever runs the reranker with a pool of sixty: one
short-gate call per search, roughly twelve thousand tokens in and a list of
numbers out, on the lane whose price is a third of the answer's. Beside the
answer call it is the only model call that reads code, and it stores nothing.
The two unique misses it does not lift are the one absent from the top 200
and the one at rank 49; a wider pool is a measurement to run, not a change to
make on this table.

## Whole-file reading

`GatherOptions.WholeFileTokens`: the file a hit sits in is read whole when it
is small (1500 tokens), and the rest of the hit's own symbol when it is not.
Written for the two unique misses that are "a constant explained one chunk
away from the hit with no symbol linking them".

| corpus | arm | result | mean sources |
|---|---|---|---|
| Go, unique gathered | product | 0.905 (38/42) | 79.5 |
| Go, unique gathered | + whole file | 0.905 (38/42) | 99.8 |
| flow, parts | product | 27/30, 7/10 whole | 164.4 |
| flow, parts | + whole file | 25/30, 6/10 whole | 194.7 |

Nothing on the Go corpus for twenty more chunks a question, and two parts
lost on the flow corpus, where the extra chunks spend the budget the crossing
needed. The two misses it was written for are in files past the size limit,
in a symbol other than the hit's, which is exactly the shape the limit
refuses. **Off.** The option stays in the code with this table as the reason.

## Route suffix matching

Measured in `2026-09-11-declared-structure.md`: 25/30 against 27/30. Off.

## Reproducing

```
hack/run-eval.sh 'TestEvalRankOfMisses$'
hack/run-eval.sh 'TestEvalMeasureRerank$'          # one short-gate call per unique question
hack/run-eval.sh 'TestEvalMeasureGathered$'        # the whole-file arm is the last one
hack/run-flow-eval.sh 'TestFlowGathered$'
```
