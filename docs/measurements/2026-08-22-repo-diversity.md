# Per-repository diversity in the fused list, and a baseline that did not reproduce

The corpus-swap document ended with a named next step: stop one repository
filling the top 20 so a second implementation has room. The number it had to
move was **7 of 16** — how many ambiguous questions retrieve *both* of their
alternatives.

Two findings, and the second is the more important one.

## Method

`TestEvalMeasureDiversitySweep` in `backend/internal/retrieve/eval`. Same
corpus, same frozen expansions (`expansions.json`), same cut as the routing arm
(K = 20, 40 candidates per lane), `text-embedding-3-small` at 1536.

Corpus indexed for this run: go-sqlite3 (293 files, 2180 chunks), peeq
(526 / 5694), rongo (143 / 1366). Indexing took 312 s.

The arm sweeps `FuseWeightedDiverse`'s decay: a repository's nth hit is ordered
as if its score were `score*decay^n`, applied before the cut to K. 1.0 is off
and is measured in the same run rather than quoted, which is what turned up the
second finding.

Four numbers per arm. One is meant to rise — ambiguous questions with *every*
alternative retrieved. Two must not fall: recall@20 on the unique cohort, and
all-parts-found on the composition cohort, where a lost part is worse than on
an ambiguous question because no clarification recovers it.

| decay | ambiguous, all | ambiguous, parts | unique r@20 | composition, all |
|---|---|---|---|---|
| **1.00 (off)** | **0.750 (12/16)** | **0.875 (28/32)** | **0.769 (30/39)** | 0.200 (1/5) |
| 0.90 | 0.812 (13/16) | 0.875 (28/32) | 0.744 (29/39) | 0.200 (1/5) |
| 0.80 | 0.812 (13/16) | 0.875 (28/32) | 0.692 (27/39) | 0.200 (1/5) |
| 0.70 | 0.750 (12/16) | 0.844 (27/32) | 0.692 (27/39) | 0.200 (1/5) |
| 0.60 | 0.750 (12/16) | 0.844 (27/32) | 0.667 (26/39) | 0.200 (1/5) |
| 0.50 | 0.750 (12/16) | 0.844 (27/32) | 0.667 (26/39) | 0.200 (1/5) |
| 0.30 | 0.750 (12/16) | 0.844 (27/32) | 0.667 (26/39) | 0.200 (1/5) |

## Finding 1: the premise was the unexpanded number

The premise was 7 of 16. With the decay switched off it measures **12 of 16**,
and 28 of 32 alternatives rather than 14 of 32 — so the constraint the change
was built to relieve is much smaller than the corpus-swap document made it look.

The corpus is not the explanation. `unique` recall@20 comes out at 0.769 here,
which is the corpus-swap document's own control value to three decimals, so
both runs are looking at the same index.

What differs is the query. `TestEvalMeasureGathered` on the same corpus reports
the ambiguous cohort at **7/16 without query expansion** and **12/16 with it**,
which is exactly the gap between the two documents. The published 7 of 16
belongs to the *raw* question — the condition the anchor cohort is measured in,
and the one the product does not run, because step 1 expands before the lanes
ever see the question.

That matters beyond this arm: **a retrieval number is only comparable to
another number measured under the same query condition.** Two conditions live
in the same harness, one of them is what ships, and the difference between them
here is five of sixteen questions — larger than anything the decay does.

## Finding 2: the decay does not earn its keep

At its best setting it buys one ambiguous question and pays for it with one
unique one — 13/16 against 29/39, versus 12/16 against 30/39. That is a wash,
not an improvement, and it is a wash bought with a knob nobody can tune without
re-running this arm.

Below 0.8 both columns fall together: the unique cohort loses three questions
by 0.6 and the ambiguous side gives its gain straight back. The mechanism is
doing what it says — pushing a repository's repeats down — and on this
catalogue the repeats being pushed down are frequently the answer.

Composition sits at 1 of 5 in every arm, untouched by the decay. It is the
worst number on the page, and gathering does not rescue it either:
`TestEvalMeasureGathered` on this corpus lifts the parts from 2/10 to 6/10 with
the symbol walk, and still finishes at 1 of 5 questions. Four of five
composition questions never get all their parts — from search, from two hops,
or from expansion. In all four the *same* candidate is the one missing, the
first, while the second arrives via the walk.

That is a defect against the design rather than a ranking problem: a
composition question is the case where asking would be wrong and every part
belongs in the answer. It wants its own diagnosis — is the missing candidate
absent from the fused list, or is it cut by the hop budget or the token cap —
and n = 5 makes it a lead, not a statistic.

## What ships

Nothing. `DefaultRepoDecay` stays 1.0 — the mechanism is in the code, switched
off, with this document as the reason. Deleting it would mean rewriting the
arm the next time the question comes up, and it will: the shape being measured
here is corpus-dependent, and a corpus with genuinely overlapping repositories
— several products that really do implement the same mechanism — is the case
where a repository crowding out the list is more than a possibility on paper.

Before anyone re-runs it, re-measure the baseline in the same run, and record
which query condition it was measured under. That is the only reason this
measurement caught that its own premise was a number from the other one.
