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

## Finding 1: the baseline did not reproduce

The premise was 7 of 16. Today, with the decay switched off, it is **12 of 16**,
and 28 of 32 alternatives rather than 14 of 32 — the constraint the change was
built to relieve is largely not there any more.

Nothing in the retrieval path explains it: this arm searches exactly as the
routing arm does, same K, same candidate depth, same frozen expansions. What
moved is the corpus. rongo indexes itself, and rongo on 22 August is not rongo
on 20 August — the swap measurement ran while the repository had just been
added. A corpus that indexes a repository under active development re-dates
its own baseline, which is worth stating plainly: **every number in these
documents is only comparable to a number measured on the same day's corpus.**

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
worst number on the page and it is not this change's to fix: a composition
question needs every part, and a fused list of twenty is evidently not where
all the parts are. That belongs to gathering, not to ranking.

## What ships

Nothing. `DefaultRepoDecay` stays 1.0 — the mechanism is in the code, switched
off, with this document as the reason. Deleting it would mean rewriting the
arm the next time the question comes up, and it will: the shape being measured
here is corpus-dependent, and a corpus with genuinely overlapping repositories
— several products that really do implement the same mechanism — is the case
where a repository crowding out the list is more than a possibility on paper.

Before anyone re-runs it, re-measure the baseline in the same run. That is the
only reason this measurement caught its own premise going stale.
