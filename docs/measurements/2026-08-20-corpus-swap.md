# loom leaves, rongo enters — and the benchmark gets harder on purpose

**In short.** The corpus swapped loom for rongo. Every number moved, and the series that phases
2 through 4c belonged to is closed: the old anchor values no longer apply and this document
sets new ones. The important finding is not any single number but why they all fell —
**loom made ambiguity artificially easy.** Two products built from one template produce
candidates that tie on score and look identical in their top chunks. Real ambiguity, the same
job solved twice by different hands, is much harder to see, and the router is now well below
the do-nothing baseline on it.

## Why the corpus changed

loom was peeq's wholesale twin. That made the `ambiguous` cohort trivial to construct — twelve
questions whose two answers were byte-identical functions, comments included — and it made the
catalogue lopsided: 44 of 61 questions wanted no clarification, so a router that never asks
scored 0.803 and nothing built could easily beat it.

rongo overlaps peeq only where it has to: retrieval, storage, auth, scheduling. That is enough
for a real ambiguous cohort and it has a second advantage — the questions about rongo can be
verified against code that is right here rather than against a sibling nobody is editing.

## What indexing rongo actually cost, twice

Two mistakes, both worth recording because both are silent.

**A local `clone_url` indexes the checkout's opinion, not the published code.** rongo was first
listed as `/Users/jan/localgit/rongo`, and cloning a local path takes that checkout's own
branch ref. The shared checkout's `master` was months behind `origin/master`, so rongo entered
the corpus at **4 files / 6 chunks** — a commit from before it had a backend. It is listed by
its remote now, and `repos.example.yaml` says why.

**`go test` cached an evaluation arm and replayed the previous run's numbers.** After fixing
the clone URL the re-index reported the same 4 files, with the same duration to two decimals,
because Go considered the package unchanged: these arms read the evaluation database, the
clones under `BACKEND_REPO_ROOT` and a live model endpoint, none of which the test cache
tracks. `hack/run-eval.sh` now passes `-count=1`, and that is not optional — a measurement that
can silently report an older run under a new heading is worse than no measurement.

## The corpus

| repository | branch | files | chunks |
|---|---|---|---|
| peeq | master | 526 | 5694 |
| rongo | master | 142 | 1358 |
| go-sqlite3 | main | 293 | 2180 |

9232 chunks against the previous 11720: loom took 3855 with it and rongo brought 1358.

## The catalogue

60 questions — 39 `unique`, 16 `ambiguous`, 5 `composition`. Thirteen questions moved their
loom half to a verified rongo counterpart, two were reworded because their own text said loom,
one was dropped for having no counterpart at all (rongo has no OIDC), and fourteen loom-only
questions were replaced with new ones about rongo.

**Five questions changed classification without anyone touching them.** `retrieve/hybrid.go`,
`retrieve/stopwords.go`, `embed/client.go` and `sched.Jittered` answer questions that used to
be unique to peeq, so those questions now have two honest answers. That is the kind of change
a corpus swap makes that a diff does not show.

The consequence is the point: **a router that never asks now scores 0.733, not 0.803.** The
benchmark is less biased toward silence and correspondingly better at telling a good router
from a mute one.

### The new questions are phrased from the reader's side, and it shows

rongo's domain vocabulary *is* its code vocabulary, so a question worded the way the code is
worded would give the understanding step nothing to bridge and flatter every retrieval number.
The new questions avoid that deliberately — „Why is a heavily used helper not in every
answer?" rather than „What does `maxDefiners` do?". The expansion arm shows what that
buys:

| cohort | raw question | expanded |
|---|---|---|
| the 14 new rongo questions | 4/14 (0.286) | **10/14 (0.714)** |
| the 25 unchanged questions | 17/25 (0.680) | **20/25 (0.800)** |
| all 60, recall@20 | 0.483 | **0.783** |

A gain of **+0.43** on the new questions against **+0.12** on the old ones. They exercise the
`code_terms` bridge instead of matching the code's own words, which is what the catalogue is
for.

## The new anchor

The old anchor cannot survive a corpus change: 10 of its 28 questions were about loom, and the
19 that remain compete against different distractors. **0.679 / 0.786 / 0.476 belongs to the
closed series.** The new values, over the 19 survivors, first candidate only:

| anchor: 19 questions, first candidate only | value |
|---|---|
| recall@5 | **0.474** |
| recall@20 | **0.632** |
| MRR | **0.145** |

They are lower than the old ones and that is expected twice over: the anchor searches the *raw*
question with no expansion, and the nine questions it lost were mostly ones that were being
found. The cohort-level control is healthier — `unique` recall@20 with expansion is 0.769,
against 0.750 on the old corpus.

Every later phase must reproduce these three numbers before any new figure it reports means
anything.

## Routing: below the do-nothing baseline

| judge | overall | `ambiguous` | `unique`+`composition` |
|---|---|---|---|
| ShortGate (non-Pro) | 0.617 (37/60) | 0.000 (0/16) | 0.841 (37/44) |
| Pro | 0.633 (38/60) | 0.062 (1/16) | 0.841 (37/44) |
| *never ask* | *0.733 (44/60)* | *0/16* | *44/44* |

The margin sweep is flat: 0.667 at 0.10 and 0.633–0.650 everywhere from 0.15 to 0.40. Moving
the threshold across its whole useful range changes at most two questions in sixty.

Grounding over the 34 `unique` questions the router did not ask about is 0.882, against 0.955
published and 0.946 in phase 4c. The router asked about only 5 of 39, so that is a gathering
number rather than a routing one: the new rongo questions are harder to gather from.

## Why routing collapsed, and what it means

Three readings agree, so this is not a guess:

- Of the 16 `ambiguous` questions, only **7 retrieve both alternatives into the top 20** —
  *(Corrected 2026-08-22: 7/16 is the RAW question. With the query expansion the product
  actually runs it is 12/16 on this same corpus — see
  `2026-08-22-repo-diversity.md`. The reading below still holds, but the gap it describes
  is smaller than 7/16 suggests.)*
  14 of 32 candidates. The router cannot ask about an ambiguity it never retrieved.
- The judge is therefore rarely reached at all.
- The margin sweep is flat, which is what one expects when the threshold's job — arbitrating
  between two close candidates — mostly has no second candidate to arbitrate.

**The binding constraint has moved from routing to retrieval.** Tuning the router against this
catalogue would optimise a decision over candidates that are not in the list. Phase 4c's own
conclusion pointed the same way from the other side: the candidates are wrong more often than
the decision about them is, and now they are frequently absent rather than merely wrong.

That reframes the phase 4b and 4c numbers as well. Against loom, both alternatives were
retrieved because they were the same text; the router's task was to judge two things it could
see. It never had to cope with the ordinary case, which is that the second implementation is
somewhere below the cut.

## Next

Per-repository diversity in the fused list: stop one repository filling the top 20 so a second
implementation has room. It belongs in `FuseWeighted`, it is measurable on this catalogue
without touching the router, and the number it has to move is 7 of 16 — how many ambiguous
questions retrieve both of their answers.
