# Composition is 4 of 5, and the missing part is a search miss

`2026-08-22-repo-diversity.md` recorded composition at **1 of 5** and every
document since quoted it as the last place a retrieval retry could still
recover something. Measured through the pipeline a reader actually gets, it is
**4 of 5**, 9 parts of 10.

The interesting part is why, because the first explanation this document gave
was wrong and so was the first one offered against it.

## Four arms

`TestEvalMeasureGathered`, same pinned corpus as every other measurement this
week (rongo `0b989b6`, peeq `bb04021`, go-sqlite3 `3fa3f30`).

| arm | composition, all parts | parts | unique recall | ambiguous, ≥2 alternatives |
|---|---|---|---|---|
| raw question, 0 hops | 0.000 (0/5) | 2/10 | 0.810 (34/42) | 0.938 (15/16) |
| raw question + walk | 0.400 (2/5) | 6/10 | 0.857 (36/42) | 0.938 (15/16) |
| **expanded + walk (the product)** | **0.800 (4/5)** | **9/10** | **0.905 (38/42)** | **0.938 (15/16)** |
| expanded + walk, no doc decay | 0.800 (4/5) | 9/10 | 0.905 (38/42) | 0.938 (15/16) |

## What changed, and what did not

**Not doc demotion.** #87 defaulted `DocDecay` on (`DefaultDocDecay = 0.7`), and
since composition candidates are all `.go`, demoting docs could plausibly have
lifted them. The fourth arm turns it off, which is how retrieval behaved before
that commit, and every number is identical — 4/5, 9/10, and the same recalls on
both other cohorts. That arm exists for exactly this: telling a result that
changed because the CODE changed from one that changed because the MEASUREMENT
did.

**The query.** `2026-08-22`'s sentence reads "`TestEvalMeasureGathered` on this
corpus lifts the parts from 2/10 to 6/10 with the symbol walk, and still
finishes at 1 of 5 questions". Those two part-counts are reproduced exactly here
by the **raw question + walk** arm. The product does not run the raw question:
step 1 expands it, and the expanded arm reaches 9/10. The walk was in the old
number all along; the expansion was not.

**The corpus, for the older figure.** `2026-08-17-gathered-set-and-catalogue.md`
does report an expanded + walk arm at 0.200 (1/5), 6/10 — but that predates the
2026-08-20 corpus swap and was measured against loom, which rongo replaced. It
is not a reading of this catalogue.

One discrepancy stays open and is not worth another run: `2026-08-22` puts the
raw + walk arm at 1 of 5 whole questions where this measures 2 of 5. One
question, on a cohort of five, from a document that reported the figure in
passing rather than as its subject.

## Which parts arrive, and how

Every composition question here pairs a repo's `store.go` with go-sqlite3's
driver internals, so the shape repeats: the local half is retrieved, the
dependency half is reached by the symbol walk.

**4 parts arrive by search, 5 by the walk, 1 never arrives.** The one miss is
peeq's `store.go` for "Are foreign key constraints active, and who turns them
on?" — rank 0, never retrieved. The walk still brings in the go-sqlite3 half, so
that turn answers from the dependency without the caller.

## The answer this arm was written for

- **Gather misses: 0.** Not one part ranked inside the search hits and then
  failed to reach the sources. The hop budget, the token budget and the eviction
  rule cost this cohort nothing.
- **Search misses: 1**, of ten parts, on one question of sixty-five.

`2026-09-06-routing-cost-metric.md` closed the other cohorts — gathering on
answered questions is 1.000 — and this closes the last one. A sufficiency gate
with a retry, the design that looked most promising before any of this was
measured, has one part to recover and would pay for a second search on every
question to find it.

## The methodological point, stated more carefully than the first draft

Two published numbers were corrected this week by measuring under the condition
the product runs, and both times the missing condition was the **query
expansion**, not the walk and not the decay:

- ambiguous questions retrieving both alternatives: **7/16 → 12/16**
  (`2026-08-22`, correcting `2026-08-20`).
- composition, all parts: **1/5 → 4/5** (here, correcting `2026-08-22`).

A third correction the same week is a different animal and should not be filed
with these: routing went from "below the never-ask baseline" to "the cheapest
setting measured" (`2026-09-06-routing-cost-metric.md`) with no re-measurement
at all — the decisions were identical and only the scoring changed.

The first draft of this document called the old figure "search-only". It was
not; the walk was in it. Getting the correction's own cause wrong, in a document
about measuring under the wrong condition, is the joke that writes itself. The
rule that survives: quote the arm, not just the number, and when a figure moves,
rule out the code before crediting the method.

## Also here: `BACKEND_ROUTE_MARGIN` is inert

`.env.example` described the margin as "how far the leading candidate must lead
before a turn answers instead of asking back" and pointed at
`2026-08-18-routing.md`, superseded twice since. The sweep is flat at 47/65
across the whole useful range, 0.10 to 0.40, and the margin rung settles no
decision either way (`2026-09-06-routing-rerun.md`) — the rungs above it reach
the answer first.

The comment says so now and names the rung that does decide. The variable stays
configurable: a corpus with different score distributions can put decisions back
on it. It is the knob a reader is most likely to reach for, and it was the one
documented by its stale rationale.
