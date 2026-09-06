# The routing metric was pricing a wasted click like a wrong answer

Every routing table on record scores accuracy: a needless card and a wrongly
composed answer each count as one error. The product does not believe that. The
judge's own prompt says so in as many words (`ask/route.go`, `judgeSystem`):

> When in doubt "ask": a follow-up question costs one click, an answer composed
> across independent mechanisms is simply wrong.

So the harness has been ranking routing settings by a rule the product
contradicts, and the consequences are not cosmetic.

**Nothing about routing changes here.** This is the same data, scored honestly.

## What the unweighted metric did

49 of the 65 catalogue questions want no card. A router that never asks
therefore scores **0.754 (49/65)** while being useless, and the shipping ladder
scores 0.723 (47/65). Read as accuracy, the ladder does not earn its keep -
which is what `2026-09-06-routing-rerun.md` and `2026-09-06-repo-rung.md` both
say, and it is what made the tighter bars look promising: `bar >= 0.80` and
`bar >= 0.85` both reach 50/65 or better, above the never-ask baseline.

The metric rewards silence, and every threshold tuned against it is pulled
toward asking less.

## Priced apart

Cost = (needless cards) + W x (missed ambiguities), where a needless card is 1.
Both arms now report this, swept over W, plus the exchange rate at which a
setting overtakes never asking.

| setting | cards | missed | W=1 | W=1.5 | **W=2** | W=3 |
|---|---|---|---|---|---|---|
| **baseline (shipping)** | 16 | 2 | 18.0 | 19.0 | **20.0** | 22.0 |
| bar &ge; 0.50 | 15 | 3 | 18.0 | 19.5 | 21.0 | 24.0 |
| bar &ge; 0.60 | 15 | 4 | 19.0 | 21.0 | 23.0 | 27.0 |
| bar &ge; 0.70 | 11 | 7 | 18.0 | 21.5 | 25.0 | 32.0 |
| bar &ge; 0.80 | 8 | 7 | **15.0** | 18.5 | 22.0 | 29.0 |
| bar &ge; 0.85 | 6 | 9 | 15.0 | 19.5 | 24.0 | 33.0 |
| defer, modules | 0 | 16 | **16.0** | 24.0 | 32.0 | 48.0 |
| defer, repos | 7 | 14 | 21.0 | 28.0 | 35.0 | 49.0 |
| *never ask* | 0 | 16 | *16.0* | *24.0* | *32.0* | *48.0* |

Counts are from one run of both arms, made for this document. Rows whose turns
reach the judge move between runs by a question or two - across the three runs
now on record `defer, modules` has read 0/16, 0/16 and 0/15 missed, and
`defer, repos` 7/14 and 8/14 - which is the re-roll `2026-09-06-routing-rerun.md`
measured. **The baseline has been 16 cards and 2 missed in every one of them**,
because the repository rung settles a spanning turn before the judge is reached.
The setting this document defends is the one that does not move.

Bold marks the best column entry. At W = 1 the winners are the settings that
barely ask; from W = 2 the shipping ladder is the **cheapest setting measured**,
and it stays cheapest at every higher price.

## The crossovers

All from the single run above, since a crossover computed across two runs mixes
the judge's re-roll into the arithmetic:

- The ladder beats **never asking** and **defer** once a wrong compose costs
  more than **1.14** needless cards - both cost 16 missed and no cards in this
  run, so they share a crossover.
- It beats `bar >= 0.85` above **1.43** and `bar >= 0.80` above **1.60**. Those
  are the two highest, so **1.60 is where the ladder becomes the cheapest
  setting on the page**, and it stays cheapest above that.

The bar rows move between runs and the baseline's do not: the repository rung
settles a spanning turn before the judge is reached, so the baseline's 16 and 2
are the same in every run on this corpus, while a setting that drops turns into
the judge's lap inherits its one-to-two-question spread. An earlier run put the
`bar >= 0.85` crossover at 1.67 rather than 1.43. The conclusion is not
sensitive to that: every value seen sits between 1.4 and 1.7.

Nobody has to defend a specific W. The whole range anyone would argue for sits
above 1.7, and the product's own prompt asserts the ratio is far larger: one
click against an answer that is simply wrong.

## What this changes

1. **The ladder earns its keep.** The standing conclusion in two merged
   documents - that routing sits below the do-nothing baseline - is an artefact
   of the metric, not a property of the ladder. Same decisions, same corpus,
   same runs.
2. **`bar >= 0.80` is retired properly.** Under accuracy it was one of the
   settings that cleared never-ask - `bar >= 0.85` and, in one run,
   `defer, modules` did too - and it was the obvious thing to ship. Priced, it
   costs 22.0 against the shipping ladder's 20.0: it buys 8 fewer needless cards
   with 5 more silently wrong answers. Note what it is *not*: it still beats
   never-asking at every W a reader would argue for (crossover 0.89). The claim
   is only that it is worse than what already ships. The pre-registered
   ambiguous criterion caught it, but by the luck of where a threshold sat
   rather than by the metric saying what it cost.
3. **The repository rung's 16 false cards are cheap.** They are the error the
   ladder is *supposed* to prefer. That does not make them free - 16 of 65 is a
   lot of clicks - but it does mean the rung is failing in the safe direction,
   and a future fix has to keep the missed-ambiguity count at 2 or it is not a
   fix.

## What it does not change

The rung breakdown, the accuracy figures, and every conclusion about GOAP-style
planning, retrieval retries and the judge. Those rest on which decisions were
made, not on how they were added up. In particular the judge still owns 0 to 1
wrong decisions, deferring to it still collapses the ambiguous cohort, and
gathering on answered questions is still 1.000.

## Caveat

W is a stand-in for a reader's patience and nobody has measured it. The claim
here is only that the ranking is stable across the whole plausible range, not
that any particular W is right. If someone ever finds that readers abandon a
thread after a needless card, W drops toward 1 and this table has to be read
again - which is why both arms print the sweep rather than a single score.
