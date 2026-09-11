# The crossing reaches the answer: edges wired into Gather

**Status: built and measured 2026-09-11. On the flow corpus the product path
goes from 20/30 parts and 5/10 whole questions (symbol walk alone) to 26/30
and 7/10. On the pinned Go corpus every published gathered number reproduces
to the question. Ships.**

## What was built

`docs/measurements/2026-09-10-integration-edges.md` measured a composed walk —
in-repo hop, crossing on a shared queue name or route, in-repo hop — at 20 of
29 flow parts, and nothing in the product ran it: `edges.Reach` was called by
the harness only, and `ask.Gatherer` followed symbols and stopped at the
repository boundary.

`Gather` now crosses. After the symbol walk it consults `edges.Neighbours`
from every gathered file, lands on the chunk holding the far side's literal
(never the whole file), and takes one symbol hop from there — the far end of a
queue is a Spring configuration class, the file that does the work is the
handler it wires up. A crossing does not count against `MaxHops`: it is bounded
by the spread ceiling (three repositories per token) and lands on one chunk,
where a symbol hop fans out. The source carries its reason,
`edge:destination shipping-task from shipping/…/ShippingController.java`, and
the answer prompt prints it as "reached in another repository, which shares
the destination shipping-task from …", so the model can say how two services
connect rather than guessing.

Two other things landed with it:

- **One definer ceiling.** `ask` walked at four definers, `edges.Reach` at
  eight, and reach.go's comment called them the same rule. They are one
  constant now, `edges.MaxDefiners = 4`, and `TestFlowEdgeReach` measures the
  same 20 of 29 at four as it did at eight.
- **Tests come last within a hop.** The composed-walk measurement reached
  `payment/service.go` through `component_test.go`. `mechanismFirst` orders
  each hop's candidates so test paths are taken after everything else — the
  same judgement `retrieve.DefaultTestDecay` makes at retrieval.

## The first wiring did nothing, and the number said so

The first version crossed after each hop's references, under the shared
budget. `TestFlowGathered`, a new arm that runs the product's search-then-
gather over the ten flow questions and scores which parts are among the
sources:

| arm | parts | questions whole | mean sources |
|---|---|---|---|
| search only | 15/30 | 3/10 | 20.0 |
| symbol walk | 20/30 | 5/10 | 158.9 |
| symbol walk + crossings, shared budget | **21/30** | 6/10 | 160.1 |

One part. The trajectories showed why: twenty hits fanned out to 141 sources
at the FIRST symbol hop on a Spring corpus, the 24000-token budget was gone,
and the crossing never ran. The edge table was consulted for nothing.

## The crossing reserve

The symbol walk now stops at the budget less one sixth (`crossingReserve`,
4000 of 24000 tokens); crossings and their far-side hop run under the whole
budget. What that buys on the flow corpus:

| arm | parts | questions whole | mean sources |
|---|---|---|---|
| search only | 15/30 | 3/10 | 20.0 |
| symbol walk | 20/30 | 5/10 | 158.9 |
| **symbol walk + crossings (the product)** | **26/30** | **7/10** | 163.7 |

Per question, the product arm:

| question | walk | product | how the new parts arrived |
|---|---|---|---|
| customer places an order | 2/6 | **4/6** | `payment/transport.go` by reference from the properties class; `ShippingController` by crossing on `/shipping` |
| Welche Dienste fragt der Bestelldienst ab | 2/5 | **5/5** | `user/api/transport.go`, `payment/transport.go`, `ShippingController` — three crossings from `OrdersConfigurationProperties` |
| Konto und Kartendaten | 1/2 | **2/2** | `user/api/transport.go` by crossing from `front-end/api/user/index.js` |
| address stored and read | 1/2 | 1/2 | the crossing from `front-end/api/user/index.js` fires only when that file is gathered, and under the reserve it no longer is |
| the other six | unchanged | unchanged | |

The two parts the flagship flow still misses, `payment/service.go` and
`ShippingTaskHandler`, are each one symbol hop past a crossing that landed;
the budget is spent before the far-side hop takes them. That is the honest
ceiling at 24000 tokens, and it is a budget question, not an edge question:
the file is in reach and there is no room to read it.

## What the reserve costs on a corpus with no edges

The Go corpus (peeq, rongo, go-sqlite3 at `pin20260820`, 65 questions) has
one manifest edge and no queue, so every crossing there is a no-op and the
reserve is pure cost. `TestEvalMeasureGathered`, all five arms in one run,
the index rebuilt from the pinned clones (526/5694, 142/1358, 293/2180 —
identical to the published counts):

| arm | unique gathered | ambiguous, both | composition, all | mean sources |
|---|---|---|---|---|
| raw, 0 hops | 0.810 (34/42) | 0.938 (15/16) | 0.000 (0/5) | 20.0 |
| raw + walk | 0.857 (36/42) | 0.938 (15/16) | 0.400 (2/5) | 78.4 |
| **expanded + walk (the product)** | **0.905 (38/42)** | **0.938 (15/16)** | **0.800 (4/5)** | 79.5 |
| expanded + walk, no doc decay | 0.905 (38/42) | 0.938 (15/16) | 0.800 (4/5) | 78.6 |
| expanded + walk, crossings off | 0.905 (38/42) | 0.938 (15/16) | 0.800 (4/5) | 95.3 |

Every recall reproduces `2026-09-06-composition-parts.md` to the question. The
reserve costs sixteen reference chunks per question and not one expected
file: the symbol walk had already found what it was going to find inside the
first 20000 tokens.

## What this does not settle

- Two flagship parts sit one hop past a landed crossing with no budget left.
  A larger budget, or a walk that takes the far-side hop before the near
  side's second symbol hop, is a measurement to run, not a change to make on
  the strength of this one.
- The address question lost its crossing to the reserve's reshuffle: one
  part, one question, and the aggregate moved six the other way.
- Both corpora were measured once. The crossing and the walk are
  deterministic, so a re-run reproduces them exactly; the expansions are
  frozen (`flow-expansions.json`), so the model's mood is out of it.

## Reproducing

```
hack/run-flow-eval.sh TestEvalIndex            # once, builds the flow corpus
hack/run-flow-eval.sh TestExpandFlowQuestions  # once, freezes the expansions
hack/run-flow-eval.sh TestFlowGathered
hack/run-flow-eval.sh 'TestFlowEdgeReach$'
hack/run-eval.sh 'TestEvalMeasureGathered$'    # the Go corpus, pinned
```

`hack/run-flow-eval.sh` is committed now; the earlier documents named a
script that lived in a scratchpad and is gone. The flow manifest is
`backend/internal/retrieve/eval/flow-repos.yaml`, pointing at the pinned
clones under `/tmp/sockshop-pin`.
