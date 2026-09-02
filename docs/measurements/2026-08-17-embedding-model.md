# Embedding model: `text-embedding-3-small` vs `-large`

**Date:** 2026-08-17
**Decision:** `text-embedding-3-small` at 1536 dimensions.
**Status:** settled. Reopen only with a larger question set — see "When to revisit".

## What was measured

28 questions (`backend/internal/retrieve/eval/questions.json`) against the dev corpus, indexed
twice — once per model, into separate database files, so the `(content_hash, model)` cache key
forced a genuine re-embedding rather than reuse of the first model's vectors.

| | |
|---|---|
| Corpus | peeq (523 files / 5685 chunks), loom (530 / 3855), go-sqlite3 (292 / 2186) |
| Total | 1345 files, 11 726 chunks |
| Retrieval | hybrid: vec0 KNN + FTS5 ladder, weighted RRF (k=60), max distance 1.25 |
| Questions | 19 in prose, 4 bare identifiers, 5 spanning two files; every expected path read and verified |
| Harness | `BACKEND_EVAL=1 go test -run TestEval ./internal/retrieve/eval/` |

## Results

| Metric | `-small` (1536) | `-large` (3072) |
|---|---|---|
| recall@5 | **0.679** (19/28) | **0.714** (20/28) |
| recall@20 | **0.786** (22/28) | **0.857** (24/28) |
| MRR | **0.476** | **0.538** |
| Indexing time | 450 s | 675 s |
| Embedding cost (this corpus) | ~$0.14 | ~$0.91 |
| Vector storage | 1× | 2× |

## Is the difference real?

No — not at this sample size. The two models were run against the same questions, so the
comparison is paired and McNemar's test applies to the discordant questions:

- **recall@20**: `-large` gained 3 questions (`freebytes.go`, `monitor.go`,
  `playbackgrant/store.go`) and lost 1 (`rag/hybrid.go`). b=3, c=1 → exact p ≈ 0.63.
- **recall@5**: `-large` gained 2, lost 1. b=2, c=1 → p ≈ 1.0.

Neither is distinguishable from noise. MRR moves 0.476 → 0.538, which is a 13 % relative
improvement in ranking and the one number that consistently favours `-large`; with 28 questions
it rests on a handful of rank shifts.

## Decision and reasoning

`text-embedding-3-small`.

The plan's own tie-break applies: a difference inside the noise goes to the cheaper and faster
option. Beyond the tie-break, three things point the same way:

- **6.5× the embedding cost** ($0.02 vs $0.13 per 1M tokens) for a difference that is not
  measurable at this sample size.
- **Twice the vector storage and 50 % longer indexing** (450 s → 675 s on this corpus), on a
  design whose whole storage story is one SQLite file.
- **1536 is the schema default**, so `-small` needs no per-deployment dimension juggling.

Recording the reasoning is the point of this file: the question must not be reopened every
quarter on the basis of "large is the better model", which is true in general and not
demonstrated here.

## What the numbers actually say about retrieval

The model is not the binding constraint. **Four questions miss under both models:**

| Question | Expected |
|---|---|
| How are vector hits and full-text hits merged? | `peeq backend/internal/rag/hybrid.go` |
| Which words are removed from a question? | `peeq backend/internal/rag/stopwords.go` |
| Where does loom write an uploaded file? | `loom backend/internal/artifact/upload_path.go` |
| Can a thread have several share links? | `loom backend/internal/chat/share_store.go` |

Three of the four are the same shape: the answering file is one of several in a package that all
talk about the same subject, and the *right* one loses to its neighbours. That is a chunking and
ranking problem, not an embedding problem, and swapping models does not touch it.

Two further observations, both measured on the **semantic lane itself** (`SearchVector` at 40
candidates, bounded at 1.25 vs unbounded) rather than on `Search`'s output. That distinction is
not pedantry: `Search` truncates to K, so both lanes saturate and a difference taken there is 0
by construction — the first version of this harness reported exactly that and it meant nothing.

- **The distance bound is barely binding, and only for `-large`.** Under `-small` it removed
  nothing at all: `barred = 0/40` on every question, with the nearest chunk between 0.93 and
  1.17. Under `-large` it removed rows for 3 of 28 questions (13/40, 15/40 and 20/40), nearest
  distances 0.96 to 1.20. So the bound is not hiding a recall failure behind a constant under
  either model — but note how little headroom there is: the nearest chunk for a *successful*
  question routinely sits at 1.05-1.17, so tightening this constant much below 1.25 would start
  cutting good lanes, which is exactly the mis-calibration peeq documents having made twice.
- **Every question filled all 20 result slots** under both models. Recall is limited by ranking,
  not by the lanes returning too little.

## When to revisit

Re-run with a question set of 80–100 before treating a `-large` advantage as real; at n=28 the
95 % interval around a recall of ~0.8 is roughly ±0.15, which is wider than anything measured
here. The harness and the comparison are one command per model, so the cost of redoing this is
an hour and about a dollar.
