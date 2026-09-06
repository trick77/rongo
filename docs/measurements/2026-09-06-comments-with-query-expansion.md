# Comments in the search lanes, re-measured with query expansion

**Status: run on 2026-09-06. `BACKEND_INDEX_COMMENTS` stays `1`. Stripping
comments still costs more than it did in 2026-08-17, not less — query
expansion did not make them redundant.**

## Why it was re-opened

`docs/measurements/2026-08-17-module-ranking-and-comments.md` decided this
once and marked it revisitable, for a reason it named itself: the harness
searched with the **raw question**, and the design does not. Its closing
section says every number there "describes a configuration the design never
proposed", and singles out the bridges comments were standing in for —
*Apple TV → AirPlay*, *Platte voll → statfs, free bytes* — as work query
expansion would do instead.

Expansion exists now (`internal/ask/understand.go`, frozen per question in
`eval/expansions.json`), and every arm here reads it. Two other things
changed: the cohort grew from 28 to 65 questions, and the corpus swapped loom
for rongo. So this is not a re-run of the old table, it is the same question
asked of the configuration the product actually ships.

The standing rule is the reason to ask at all: the search mechanism searches
code, not comments. A stale comment pulls the vector towards a claim no line
of code has to honour, and an answer built on one is convincingly wrong.

## Method

Two indexes over the same corpus (peeq, rongo, go-sqlite3 at `pin20260820`),
one with `BACKEND_INDEX_COMMENTS=1` and one with `=0`, each in its **own**
database: stripping changes every content hash, so a shared one would serve
vectors computed with comments and the arm would measure nothing. Chunk
counts confirm the two indexes are the same corpus — peeq 5694 / 5681,
go-sqlite3 2180 / 2177, rongo 1358 / 1357, the small differences being window
boundaries moving when prose leaves a region.

`TestEvalMeasure` on each, in the same session, under the shipping
configuration (`DefaultDocDecay = 0.7` in both arms). Both arms measured
today rather than one of them quoted from August: an arm compared against a
remembered baseline measures the memory.

`raw_text` is untouched by stripping either way, so citations quote the real
file in both arms. Only the two search lanes change.

## Result: comments stay

| unique cohort (n=42) | recall@5 | recall@20 | MRR |
|---|---|---|---|
| **comments in the search lanes** | **0.714 (30/42)** | **0.810 (34/42)** | **0.573** |
| comments removed | 0.548 (23/42) | 0.690 (29/42) | 0.407 |

| anchor: the original 28, first candidate only (n=19) | recall@5 | recall@20 | MRR |
|---|---|---|---|
| **comments in the search lanes** | **0.789 (15/19)** | **0.842 (16/19)** | **0.518** |
| comments removed | 0.421 (8/19) | 0.684 (13/19) | 0.211 |

| | ambiguous (n=16), both alternatives in the top 20 | composition (n=5), all parts |
|---|---|---|
| comments in the search lanes | 0.938 (15/16), candidates 30/32 | 0.000 (0/5), parts 2/10 |
| comments removed | 0.750 (12/16), candidates 26/32 | 0.000 (0/5), parts 2/10 |

Seven questions leave the top 5 of the unique cohort and five leave the top 20.
On the anchor it is worse: recall@5 falls by nearly half and MRR by 59 %
relative — a larger loss than August's 29 %, on the same 19 questions.

**Query expansion did not replace what comments were doing.** The old document
guessed it might: the vector lane needs natural language to bridge a
business-language question, and a doc comment is the only natural language a
code file has. Expansion adds *guessed code vocabulary* to the query side; it
does not put domain words into the corpus for that vocabulary to meet. The two
are complements, and the arm shows it — with both in place, removing one still
costs seven questions.

The composition cohort is 0/5 in both arms, so nothing here is about it.

## What this does not settle

Unchanged from August, and still worth writing down:

- Stripping moves the vector lane and the keyword lane at once, so the loss
  cannot be attributed to one. Keeping comments for the vector lane only is a
  third arm and one more index run away.
- The correctness argument is untouched by these numbers. A stale comment is
  still a claim no code has to honour; what is now known twice is what
  removing them costs, which is seven questions the reader would otherwise
  have got an answer to.

## So what happens to "search code, not comments"

It is honoured by the two mechanisms that do not cost recall, both shipped:

- **the demotion**, `DefaultDocDecay = 0.7` — prose files cannot outrank code
  at equal standing (`docs/measurements/2026-09-05-doc-demotion.md`);
- **the diagram rule** — a node cites code or nothing, doc markers dropped
  from `src` in `renumber.go` before reader numbers are assigned.

What the rule cannot buy is a corpus with the domain vocabulary taken out of
it. A comment inside a function is part of that function's chunk, and the
answer is still written from the code around it, cited at the lines it was
read from.

## Reproducing

```
set -a; . .env; set +a
BACKEND_EVAL=1 BACKEND_INDEX_COMMENTS=0 \
BACKEND_EVAL_DB=<scratch>/eval-nocomments.db \
BACKEND_REPOS_FILE=<root>/repos.yaml BACKEND_REPO_ROOT=<scratch>/eval-repos \
go test -v -timeout 90m -run 'TestEvalIndex|TestEvalMeasure$' ./internal/retrieve/eval/
```

Then the same `TestEvalMeasure$` against the database built with comments. Its
own database and its own repo root, for the content-hash reason above.
