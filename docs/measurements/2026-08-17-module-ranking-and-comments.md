# Module-level ranking, and what dropping comments costs

Two questions, measured on the same corpus, the same 28 questions and the same
embedding model as the phase-2 measurement (`text-embedding-3-small`, 1536).

1. Does ranking by module improve retrieval? This is the thesis phase 3 rests on:
   `module_profiles` is a routing layer, and the cheapest version of that idea —
   grouping chunk hits by module — can be tested without a single model call.
2. What does "search code, not comments" cost in hit rate?

Corpus: peeq (523 files, 5685 chunks), loom (530 / 3855), go-sqlite3 (292 / 2186).
Indexing took 439 s with comments and 419 s without.

## Method

`TestEvalMeasureModules` in `backend/internal/retrieve/eval`. One search per
question at **60 candidates**, every arm cut to 20 after reranking. The depth
matters: reranking the top 20 in place leaves recall@20 identical by
construction — same set, different order — and every arm would report a
difference of zero however well it worked. That is the mistake the phase-2
`barred` metric made, and it is easy to repeat.

The acceptance criterion was fixed before the run: **the module layer is worth
building on only if an arm beats the baseline's recall@5.**

The baseline reproduces phase 2 exactly (recall@5 0.679, recall@20 0.786,
MRR 0.476), so the harness measures the same thing it did then.

## Result 1: module-level ranking is rejected

| Arm | recall@5 | recall@20 | MRR |
|---|---|---|---|
| **baseline (plain chunk ranking)** | **0.679** | **0.786** | **0.476** |
| module-sum | 0.214 | 0.429 | 0.189 |
| module-best | 0.429 | 0.607 | 0.384 |
| module-count | 0.214 | 0.429 | 0.188 |
| blend-0.15 | 0.607 | 0.786 | 0.385 |
| blend-0.40 | 0.607 | 0.786 | 0.353 |

No arm beats the baseline. Two mechanisms, both visible in the data:

**The grouping variants fail on module size.** They move every hit of the
best-standing module to the front. `peeq/backend/internal/httpapi` holds 1135
chunks across 53 files, so it accumulates standing by sheer size and buries a
single excellent hit under twenty mediocre ones. Questions that ranked 1 fall
out of the top 20 entirely.

**The blend does what it was designed to do and still loses.** It adds a
log-damped corroboration bonus to each hit's own score instead of reordering
wholesale, and on the exact failure shape phase 2 recorded it helps:

| Question | baseline | blend-0.15 |
|---|---|---|
| How are vector hits and full-text hits merged? (`rag/hybrid.go`) | 7 | **2** |
| How is a channel filter kept from taking effect only after the neighbour search? | 5 | **3** |

But it costs more elsewhere than it gains: `sanitize filename` 4 → 9, `jittered
loops` 4 → 7, and several rank-1 answers slip to 2. Two questions won, five lost.

**recall@20 does not move at all** (0.786 in both blend arms). Nothing new is
found; hits are only reshuffled. Reranking cannot fix a question whose answer is
not in the candidate set.

### A correction to the reasoning that motivated this phase

The phase-3 plan argued from "recall@20 = 0.857, so the answer is already in the
list and only badly ordered". That figure is `text-embedding-3-large`'s; under
the model actually deployed it is 0.786, so six of 28 questions miss.

**Where those six actually are** — measured afterwards with `K=200`, which is
the thing to check before calling anything a retrieval failure:

| Question | expected file | fused rank | found by |
|---|---|---|---|
| Platte fast voll | `peeq download/freebytes.go` | **not found** | — |
| Extractor-Fehler | `peeq failmonitor/monitor.go` | 34 | `keyword:any` |
| Apple TV | `peeq playbackgrant/store.go` | 52 | `keyword:any` |
| Stopwords | `peeq rag/stopwords.go` | 23 | `semantic` |
| Upload-Pfad | `loom artifact/upload_path.go` | 25 | `semantic` |
| Share-Links | `loom chat/share_store.go` | 23 | `semantic` |

**Five of the six are found and merely ranked too low**, three of them by the
semantic lane at ranks 23–25 — just outside the cut. Only `freebytes.go` is a
genuine miss. So this is a ranking problem after all, and the earlier reading of
these numbers as "the answer is not in the candidate set" was wrong: rank 0 in
the harness means "not in the fused top 20", not "not retrieved".

That the module arms do not rescue them is therefore a real result, not a
tautology — they had the chance and did not take it.

### Consequence

`module_profiles` is not built. The routing layer exists to raise answer
quality; its cheapest precursor measurably lowers it. Spending 700–7000 Pro
calls on the fuller version — one profile per module across 20–200 repositories,
re-triggered by every diff — cannot be justified on this evidence.

What survives is `internal/modules`: the clustering itself is deterministic,
costs nothing, and is what the Repos page counts. It stays. `RerankByModule`
stays too, unused by any request path, because it is what makes this measurement
repeatable when the question set grows.

## Result 2: dropping comments costs about four questions

`BACKEND_INDEX_COMMENTS=0` removes whole-line comments from both search lanes —
the embedded text and the FTS5 row — while `chunks.raw_text` keeps the untouched
source so citations still quote the real file. Verified on the two databases:
371 chunks match `deliberately` in the keyword lane with comments, 41 without
(trailing comments after code survive by design), and all 371 still carry it in
`raw_text`.

**Decision: comments stay in the search lanes.** `BACKEND_INDEX_COMMENTS`
defaults to `1`. The switch stays in the code, and so does this measurement, so
the question can be reopened with a number rather than an argument.

| Baseline arm | recall@5 | recall@20 | MRR |
|---|---|---|---|
| comments in the search lanes | 0.679 | 0.786 | 0.476 |
| **comments removed** | **0.536** | **0.679** | **0.342** |

> **Measured twice.** The first run used a stripper that also deleted code —
> it reused the prefix set built for `docStart`, so `*p = v`, `--n;`,
> `#include <stdio.h>` and `;; lisp` all vanished from the search lanes. That
> made the arm invalid, because it conflated losing prose with losing code.
> After the fix (extension-aware prefixes, a bare `*` only when a separator
> follows) the corpus was re-indexed and re-measured. The numbers came back
> unchanged: recall@5 0.536 both times, MRR 0.340 → 0.342. The deleted lines
> were not the ones carrying the distinguishing identifiers for these 28
> questions. The bug was real and is fixed; its effect on this metric was not.

Four questions leave the top 5, three leave the top 20, and MRR falls 29 %
relative. Nine questions now have no correct hit at all, against six before.

`rag/hybrid.go` is the clearest case: rank 7 with comments, **not found** without.
Its doc comments are where the words a question actually uses appear; the
function bodies talk about `rrf`, `weights` and `rowid`.

### What this measurement cannot say

Stripping changes the vector lane and the keyword lane at once, so the loss
cannot be attributed to one of them. The two are not equally suspect: a comment
is natural language, which is what the vector lane needs to bridge a
business-language question, while prose matching in the keyword lane is more
often accidental. Keeping comments for the vector lane only is a third option
and one more index run away.

The correctness argument for removing them is untouched by these numbers: a
stale comment pulls the vector towards a claim no line of code has to honour,
and an answer built on it is convincingly wrong. That is a decision about which
failure is worse, not about the hit rate, and the hit rate is now known.

## Module granularity, settled

The spec left the clustering unit deliberately open. Measured on the real
corpus with `MinChunks=8`, `MaxChunks=150`: **137 modules** — peeq 52, loom 39,
go-sqlite3 46 — of which 18 exceed the split ceiling.

The directory cut is workable but has two visible weaknesses, both recorded
rather than papered over:

- **Flat directories cannot be split.** `peeq/backend/internal/httpapi` is 53
  files in one directory, 1135 chunks; `peeq/ui/src` is 45 files, 1022 chunks.
  The split rule needs a next path level and there is none, so these stay whole
  and are marked `Oversized`.
- **The Selector does not skip dot-directories.** `peeq/.github/workflows`,
  `go-sqlite3/.github` and `loom/docs/superpowers/specs` all became modules.
  They cost nothing today because no profile is generated, but they are
  candidates nobody will ever ask about.

## What this harness does not measure

It searches with the **raw question**. The design does not: step 1 of the
question pipeline (`Verstehen`) produces *«erweiterte Suchbegriffe samt
geratenem Codevokabular»*, and the spec's «Anfrageseite» row says the query goes
in twice — once in business language, once with guessed code vocabulary — and
both result lists are merged.

That is exactly the bridge these questions need: *Apple TV → AirPlay*,
*Platte voll → statfs, free bytes*. It is designed, it is unbuilt, and it is
phase 4. Every number here therefore describes a configuration the design never
proposed, and the six failures above should be re-measured once query expansion
exists before any of them is treated as a property of the index.

Worth noting for that re-measurement: `playbackgrant/store.go` contains both
"AirPlay" and "Apple TV" **literally**, in its package doc comment. The bridge
was never missing for that question — the keyword lane holds the file at rank 15
of its OR rung, and fusion places it at 52. This is also a second, quieter
argument for keeping comments in the search lanes.

## Reproducing

```
BACKEND_EVAL=1 BACKEND_EVAL_DB=/tmp/rongo-eval-small.db \
BACKEND_EMBED_BASE_URL=... BACKEND_EMBED_API_KEY=... \
BACKEND_REPOS_FILE=<root>/repos.yaml BACKEND_REPO_ROOT=/tmp/rongo-eval-repos \
go test -v -timeout 60m -run 'TestEvalIndex|TestModuleList|TestEvalMeasureModules' \
  ./internal/retrieve/eval/
```

Add `BACKEND_INDEX_COMMENTS=0` and a different `BACKEND_EVAL_DB` for the
comment-free arm. It needs its own database: stripping changes every content
hash, so sharing one would serve vectors computed with comments and the arm
would measure nothing.

## When to revisit

At 28 questions a difference of 0.14 in recall@5 is four questions. The phase-2
document already recommends growing the set to 80–100 before treating smaller
differences as real; the comment result is large enough to act on, the module
result is not close enough for the sample size to matter.
