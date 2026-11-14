# A project chosen off a card is searched as one product

**Status: measured 2026-11-14, adopted (#369). After a card choice the
one-product decision now reads the repositories the choice resolved to, not
the stored card entry. On eight questions of one letters project: 34/46
rubric claims present (74%) against 27/58 (47%) before, contradicted 3 to 0,
forbidden 2 to 0. On the four questions with source logging both ways the
expected files reached the sources 10/12 times against 3/12. Two of the
three questions that scored zero now score in both runs.**

## What was wrong

A card's project entry stores only the members that had hits, here the
project's one repository named like the project. Resuming resolves that
entry by name and so expands it to every member: the project plus its three
`uses` libraries. The one-product check ran on the stored entry (one
repository, or that plus one library), found no whole product and ran the
resumed turn as a comparison: one search per repository, merged. Four sides
shared a cut of 40; the project itself got 1, 5 and 7 hits on three
questions, two libraries most of the rest (one mapping file many times).

A throwaway diagnostic, same database and the same expanded query texts:
one fused search over the same four repositories, at depth 20, kept 18-20
of 20 hits in the project and ranked the expected template first on two of
the three questions (eighth on the third). The shipped one-product path
searches at depth 40, which the diagnostic did not run; the answer
measurement below is what covers it.

## Change

`onlyOneProduct` takes the list that will be searched. Both callers pass the
resolved list, so a choice that resolves to a whole product, libraries
included, is one search at the project turn's depth. The stricter rule is
unchanged: a choice that resolves to members of two products, or to some
members only, stays a comparison.

## Measurement

One database (full estate, eight repositories), answer lane mimo-v2.6-pro,
gate and judge mimo-v2.6-flash, audience BA, German, cards answered with the
rubric's project. The answer model is the one every earlier estate number
was taken on (`2026-11-11-mtext.md`); the change is in retrieval and was not
re-run on another answer model.

- Before: master before the change, two calls of two runs: five questions
  (12/17, 12/17), then four with source logging (1/10, 2/14). The
  shared-condition question ran in both.
- After: master plus the change, the eight questions, one call of two runs
  (17/19, 17/27).

Per question, each run's score in order; "failed" is an answer that hit the
completion length cap (17408 tokens), "card" a module card the harness
cannot answer from the rubric. Neither is judged, so neither counts in the
totals.

| Question type | Before | After |
|---|---|---|
| which letter a case sends | 0/2, 0/2 | 2/2, 1/2 |
| archive title condition | 3/3, 3/3 | 3/3, 3/3 |
| thank-you vs refund wording | 4/4, 4/4 | 4/4, 4/4 |
| enclosure request condition | 2/2, 2/2 | 2/2, 2/2 |
| singular vs plural enclosure | failed, 0/4 (2 contradicted, 2 forbidden) | failed, 1/4 |
| bulk vs manual run | 0/4, 0/4 | 3/4, 1/4 |
| condition + block in the shared library | 3/4, 1/4, 1/4, 2/4 (1 contradicted) | 3/4, 3/4 |
| loop + block in the shared library | 0/4, 2/4 | card, 2/4 |
| **total** | **27/58, 47%, 3 contradicted, 2 forbidden** | **34/46, 74%, 0 contradicted, 0 forbidden** |

The totals pool unequal run counts: the shared-condition question has four
runs before and two after, and unjudged runs drop out. Read the rows, not
only the percentage.

Expected files gathered into the sources, on the four questions of the
source-logging call (which letter, singular vs plural, bulk vs manual,
shared condition): 1/5 and 2/7 per run before, 4/5 and 6/7 after.

## Reading

Two questions that scored zero in every run before score in both runs
after: which letter (0, 0 to 2/2, 1/2) and bulk vs manual (0, 0 to 3/4,
1/4). The shared-condition question went from 1-3 of 4 over four runs to
3/4 twice. The three questions that scored full stayed full. The
singular vs plural enclosure question rests on one judged run per arm
(0/4 to 1/4) and proves nothing either way; it now gathers its expected
files, but its answer hits the completion length cap in one run of each
arm. The next problem there is the answer, not retrieval. The module card
on the loop question is a card inside one project; one run of it says
nothing about this change.

Not fixed here: only an entry that names its project expands to every
member. A project whose repositories are all named unlike the project
resumes on the members that had hits; if a library had none, that is not the
whole product and the turn is still a comparison. Gather, locate and memory
still read the stored entry, not the resolved list (pre-existing).
