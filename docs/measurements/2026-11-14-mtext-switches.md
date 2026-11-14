# Following an M/TEXT switch through its own file

**Status: measured 2026-11-14, not adopted, code not merged. A gather pass,
seeded from search hits, that brought the chunks of a hit's own file
sharing a switch variable with it, showed no gain: 36/54 rubric claims
present (67%) against 34/46 (74%) without, 1 contradiction against 0, no
question more than one claim outside its off range. The arms differ in
which answers were judged (see the table), so read the rows. The question
it was built for scored 1/4 and 0/4, but on that question's block the pass
never found a seed: in neither run did search hit a chunk mentioning its
switches. On its target the paid run measured nothing; elsewhere it
measured the pass's side effects.**

## Why it was tried

The singular vs plural enclosure question of
`2026-11-14-resume-one-product.md` needs three chunks of an included text
block: one counting the business units and setting two switch variables,
and two rule chunks reading them to pick the singular or plural enclosure.
The question shares almost no words with them, so search does not return
them. Landing up to two more chunks of each included file, picked by the
question's words, was already measured off
(`2026-11-13-mtext-include-blocks.md`). In one run of the earlier
measurement search did hit the block's chunk after its head, which resets
both switches (the head arrived by include, that chunk as a search hit);
the switches tie it to exactly the three missing ones.

## What was built

A harness-only gather option. For each M/TEXT search hit, the other chunks
of the same file were taken (hop 0, never walked from) when they shared a
variable with the hit and:

- one side sets it (`.SET`, `.ADA`, `.ADD`, `.SUB`), so the two chunks depend
  on each other rather than both reading a global;
- at most four chunks of the file mention it, which keeps out a loop
  counter running through the file (one used in four chunks or fewer
  passes);
- no other file of the hit's repository or the turn's ceiling mentions it
  (copies of the file, same stem, not counted), which keeps shared state out.

So a hit on the reset or the counting chunk would have brought the other
three; a hit on one rule chunk only the two that set the switches, since
the two rule chunks only read them.

The pass ran after the symbol walk, under what the walk left plus a reserve
of one twelfth of the budget. That kept the pass from taking budget ahead
of the walk's includes, but the walk itself then ran on eleven twelfths of
its usual budget; sources the smaller walk refused were not counted, so
the on arm changed two things.

Checked offline before the paid run, on the saved sources of the previous
measurement: the run in which the reset chunk was a hit gained exactly the
three missing chunks, and the answers with saved sources (14 of 16: the
failed answer and the card have none) gained 0 to 9 sources each, 54 in
all.

## Measurement

One database, answer lane mimo-v2.6-pro, gate and judge mimo-v2.6-flash,
audience BA, German, cards answered with the rubric's project. Off: the
after arm of `2026-11-14-resume-one-product.md` (master with #369), whose
call ran the evening before that doc was written. On: the same build plus
the option, one call of two runs, the same eight questions, about fifteen
hours later; a model day can shift a whole call. "failed" is an answer that hit the completion length cap, "card" a
module card the harness cannot answer; neither is judged, so the off arm's
totals leave out eight claims the on arm's include.

| Question type | Off | On |
|---|---|---|
| which letter a case sends | 2/2, 1/2 | 1/2, 2/2 |
| archive title condition | 3/3, 3/3 | 3/3, 3/3 |
| thank-you vs refund wording | 4/4, 4/4 | 4/4, 4/4 |
| enclosure request condition | 2/2, 2/2 | 2/2, 2/2 |
| singular vs plural enclosure | failed, 1/4 | 1/4, 0/4 |
| bulk vs manual run | 3/4, 1/4 | 4/4, 1/4 |
| condition + block in the shared library | 3/4, 3/4 | 3/4, 3/4 (1 contradicted) |
| loop + block in the shared library | card, 2/4 | 1/4, 2/4 |
| **total** | **34/46 (74%), 0 contradicted** | **36/54 (67%), 1 contradicted** |

The pass itself added 2 to 10 sources to 14 of the 16 on answers (the
enclosure answers 8 and 6, all from other files the answer had hits in)
and none to two (one run each of which letter and bulk vs manual). That
counts what it took inside the on run; the net change against
the off arm is not paired, because the two arms' searches returned
different hits.

## Reading

Three questions land one claim outside their off range (bulk vs manual up,
loop and singular vs plural down); none by more. With two runs per arm,
in two calls, that is noise.

On the enclosure question the pass had no seed in the block: of it only the
head arrived, by include, and the head mentions no switch. The chunk after
the head, a search hit in the earlier run, was not hit in either of these. Whether search
hits a chunk mentioning the switches is chance on this question, and a pass
seeded from hits cannot change that. The negative result is for that
seeding. A pass seeded from include landings would start from every
included head, which mentions no switch here either; reaching the switches
from a head needs another lookup first. Neither was measured.

The enclosure question stays a known weak spot: its deciding lines share
almost no words with the question, and two narrow gather routes have not
reached them reliably.
