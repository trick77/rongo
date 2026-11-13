# An M/TEXT include does not also land the target's block chunks

**Status: measured 2026-11-13, not adopted, code not merged. Landing up to
two block chunks of an included file beside its head showed no gain on
eight include questions: 60% of rubric claims present against 66% without
(64% with the off arm weighted to two runs per question), the two
shared-library questions lower, more sources per answer. Within the noise of
two runs; nothing to keep it for.**

## Why it was tried

An include lands on the target file's head chunk only (title, settings). The
target's logic sits in its top-level block chunks, which arrive only when
search hits them. On the private estate of `2026-11-11-mtext.md` one question
asks which enclosure a letter names for one versus several items; the
include's target counts the items and picks singular or plural in blocks
that never reached the sources.

## What was built

A gather knob (default 0) admitted up to N further chunks of each included
file after its head: chunks matching the question's words first (bm25 over
the keyword lane's last rung, restricted to that file), then file order.
Admitted through the walk's budget like every other source; a refused chunk
was skipped, a chunk already gathered not counted. Admitted blocks joined
the frontier, so their own includes were followed one hop later.

## Measurement

One database, answer lane mimo-v2.6-pro, gate and judge mimo-v2.6-flash,
audience BA, German, project cards answered on the rubric's project.

- Off: the build of master at the time (M/TEXT support and card answering,
  no knob), on the estate's 14-question rubric: the ten of `2026-11-11-mtext.md`
  plus four migration questions. Two calls of two runs, both stopped by the
  test timeout on a slow model day, so questions have one to four scored runs.
- On: the same build with the knob at 2, the eight questions below, one call
  of two runs.

| Question type | Off (runs) | On (runs) |
|---|---|---|
| logic in the included block | 15/16, 94% (4) | 8/8, 100% (2) |
| template included in template | 6/6, 100% (3) | 4/4, 100% (2) |
| singular vs plural enclosure | 1/12, 8% (3), 4 contradicted | 2/8, 25% (2) |
| condition + block in the shared library | 6/8, 75% (2) | 4/8, 50% (2) |
| loop + block in the shared library | 2/4, 50% (1), card ×1 | 2/8, 25% (2) |
| migration: same note as today | 5/6, 83% (2), 1 contradicted | 4/6, 67% (2), 2 contradicted |
| migration: same date window | 4/6, 67% (2) | 4/6, 67% (2) |
| migration: nested condition | 2/4, 50% (1), card ×1 | 3/4, 75% (1), card ×1 |
| **total** | **41/62, 66%, 5 contradicted** | **31/52, 60%, 2 contradicted, 1 forbidden** |

Weighting each off question to two runs at its own rate gives about 33/52,
64%. Sources per answer on these questions rose from 25-101 to 46-102.

## Reading

The question the arm was built for went from 8% to 25% and lost its
contradictions, but stays wrong; the two shared-library questions fell from
75% to 50% and 50% to 25%. The arm admits more sources per answer, which is
consistent with blocks taking walk budget from references, but refused
sources were not counted, so that cause is not measured. With one to four
runs per question no type moved clearly beyond the noise. If the counting
logic of an included block is to reach an answer, a narrower route
(search, or the one block a question names) is the next thing to try, not
landing more of every included file.
