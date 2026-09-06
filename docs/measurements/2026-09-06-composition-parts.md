# Composition is 4 of 5, not 1 of 5, and the one missing part is a search miss

`2026-08-22-repo-diversity.md` recorded composition at **1 of 5** with "the same
first candidate missing in four of five", and every document since has quoted it
as the last place a retrieval retry could still recover something. This arm
measures it through the pipeline the product actually runs.

It is **4 of 5**, 9 parts of 10.

## The number depended entirely on the condition it was measured under

`TestEvalMeasureGathered` runs three arms over the same pinned corpus. Only the
third is what a reader gets.

| arm | composition, all parts | parts | unique recall | ambiguous, ≥2 alternatives |
|---|---|---|---|---|
| raw question, 0 hops | 0.000 (0/5) | 2/10 | 0.810 (34/42) | 0.938 (15/16) |
| raw question + walk | 0.400 (2/5) | 6/10 | 0.857 (36/42) | 0.938 (15/16) |
| **expanded + walk (the product)** | **0.800 (4/5)** | **9/10** | **0.905 (38/42)** | **0.938 (15/16)** |

The published 1 of 5 sits between the first two rows: it was a search-only
number, and the walk is not an optimisation the product might do - it is step 5
of every turn.

## Which parts arrive, and how

Every composition question in this catalogue pairs a repo's `store.go` with
go-sqlite3's driver internals, so the shape is the same throughout: the local
half is retrieved, the dependency half is reached by the symbol walk.

| question | part | rank | hop | how |
|---|---|---|---|---|
| two writes at the same time | peeq `store.go` | 7 | 0 | search |
| | go-sqlite3 `driver/driver.go` | - | 1 | walk |
| who registers the driver | rongo `store.go` | 2 | 0 | search |
| | go-sqlite3 `driver/driver.go` | - | 1 | walk |
| `journal_mode=wal` through the URL | peeq `store.go` | 16 | 0 | search |
| | go-sqlite3 `driver/driver.go` | - | 1 | walk |
| waiting on a locked database | rongo `store.go` | 2 | 0 | search |
| | go-sqlite3 `conn.go` | - | 1 | walk |
| foreign key constraints | peeq `store.go` | **0** | **-1** | **never retrieved** |
| | go-sqlite3 `driver/driver.go` | - | 1 | walk |

**4 parts arrive by search, 5 by the walk, 1 never arrives.**

## The answer to the question this arm was written for

- **Gather misses: 0.** Not one part ranked inside the search hits and then
  failed to reach the sources. The hop budget, the token budget and the eviction
  rule cost this cohort nothing.
- **Search misses: 1.** peeq's `store.go` never appears in the hits for "Are
  foreign key constraints active, and who turns them on?" - and note the walk
  still brought in the go-sqlite3 half, so the turn answers from the dependency
  without the caller.

So the remaining retrieval headroom in this catalogue is **one part in ten, on
one question in sixty-five**. `2026-09-06-routing-cost-metric.md` closed the
other cohorts (grounding on answered unique questions is 1.000); this closes the
last one. A sufficiency gate with a retry - the design that looked most
promising before any of this was measured - has one part to recover, and it
would pay for a second search on every question to find it.

## The methodological point, now three times over

Three published numbers have been corrected this week, all in the same
direction, all by measuring under the condition the product runs:

- ambiguous questions retrieving both alternatives: **7/16 → 12/16**, once the
  frozen expansion was used instead of the raw question (`2026-08-22`).
- composition, all parts: **1/5 → 4/5**, once the symbol walk was included
  (here).
- routing: the ladder does not clear the never-ask baseline **→ it is the
  cheapest setting measured**, once the two error types were priced apart
  (`2026-09-06-routing-cost-metric.md`).

Every one of them made the product look worse than it is, and every one was
quoted forward into later documents as a premise. A measurement that does not
run the pipeline the reader gets is not a conservative estimate; it is a
different measurement, and it belongs beside its condition or not at all.

## Also here: `BACKEND_ROUTE_MARGIN` is inert

`.env.example` described the margin as "how far the leading candidate must lead
before a turn answers instead of asking back" and pointed at
`2026-08-18-routing.md`, superseded twice since. The sweep is flat at 47/65
across the whole useful range, 0.10 to 0.40, and the margin rung settles no
decision either way (`2026-09-06-routing-rerun.md`) - the rungs above it reach
the answer first.

The comment now says so and names the rung that does decide. The variable stays
configurable: a corpus with different score distributions can put decisions back
on it. It is the knob a reader is most likely to reach for, and it was the one
documented by its stale rationale.
