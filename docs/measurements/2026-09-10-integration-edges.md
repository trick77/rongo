# Integration edges, and the in-repo hop that makes them usable

**Status: built and measured 2026-09-10 on the pinned Sock Shop corpus. Edges
alone reach 10 of 29 flow parts. Composed with one in-repo hop at each end they
reach 20 of 29, and the flagship five-repository flow goes from 2/6 to 6/6. The
nine that remain have no shared literal on either side and never will.**

## What was built

`backend/internal/edges` extracts the named destinations a file talks about —
queue and topic names, HTTP routes — as string literals standing next to a
messaging call, a route annotation or a router registration. They land in
`integration_tokens` (migration `0015`), hanging off `files(id)` with
`ON DELETE CASCADE`, written by the same `ReplaceFile` transaction that writes
chunks and symbols.

No model runs. Nothing is inferred. A token is re-derived whenever its file is
re-indexed, so it cannot age independently of the code it came from, and the
index-time cost is a regex pass over lines that already had to be read.

`edges.Neighbours` returns files in other repositories carrying the same token,
with a ceiling of three repositories per token: a value present in most of the
estate is a convention, not a link. A wrong edge is worse than a missing one,
because retrieval follows it and the answer then cites what it found.

`edges.Reach` composes: one in-repo hop, the crossing, one in-repo hop. The
in-repo hop is the rule `internal/ask` walks references by — whole identifiers,
definer ceiling of eight counted across enabled repositories — applied in BOTH
directions, because the corpus needs both: forward finds the configuration class
a controller calls into, backward finds the handler that a queue's configuration
wires up. Fan-out is capped and every hop is ordered, so the walk is bounded and
its trail is the same on every run.

## The boundaries are crossed

`TestFlowEdgesCrossRepositories`:

| from | to | via |
|---|---|---|
| `shipping/…/ShippingController.java` | `queue-master/…/RabbitMqConfiguration.java:18` | destination `shipping-task` |
| `queue-master/…/ShippingConsumerConfiguration.java` | `shipping/…/RabbitMqConfiguration.java:18` | destination `shipping-task` |
| `orders/…/OrdersConfigurationProperties.java` | `payment/transport.go:29` | route `/paymentAuth` |

The last is Java to Go, across two repositories, on a string literal. No import,
no shared type, no symbol.

The table is also clean. Eleven tokens are shared by more than one repository
and every one spans exactly two:

```
route       /addresses /cards /customers /login /orders /paymentAuth
            /register /shipping /tags
destination shipping-task, shipping-task-exchange
```

Those are the estate's real boundaries and nothing else got in. `/health`, which
all eight repositories serve, is excluded by name.

## Edges alone stop one file short

`TestFlowEdgeReach` asks what matters: standing on one part of a flow, do the
other parts come within reach?

| question | edge only | by repository | composed |
|---|---|---|---|
| customer places an order | 2/6 | 3/6 | **6/6** |
| Wer wird benachrichtigt … Versand | 3/4 | 4/4 | **4/4** |
| guest cart on login | 0/3 | 0/3 | 0/3 |
| Zahlung abgelehnt | 0/2 | 0/2 | **2/2** |
| Konto und Kartendaten | 2/2 | 2/2 | 2/2 |
| address stored and read | 0/2 | 0/2 | 0/2 |
| Welche Dienste fragt der Bestelldienst ab | 3/5 | 4/5 | **4/5** |
| queue unavailable | 0/2 | 1/2 | **2/2** |
| Rechnungsbetrag | 0/3 | 0/3 | 0/3 |
| **catalogue** | **10/29** | 14/29 | **20/29** |

Every miss of the edge-only column had one shape: the literal was in a file NEXT
TO the one an answer needs. `OrdersController.java` is the file the answer needs
and it calls `config.getPaymentUri()`; the string `/paymentAuth` lives in
`OrdersConfigurationProperties.java`. On the far side the same thing again: the
crossing lands on `RabbitMqConfiguration`, while the work is done by
`ShippingTaskHandler`, wired by Spring, carrying no literal.

The composed walk is that observation implemented. Its trail, logged by
`TestFlowComposedWalkReachesTheFileThatMatters`:

```
OrdersController.java
  --in-repo--> OrdersConfigurationProperties.java
  --edge "/paymentAuth"--> payment/transport.go
  --in-repo--> payment/service.go
```

Worth putting next to the loop diagnostic: the twelve-round tool loop reached
5 of 6 parts on that same question, at 77-83k tokens. The composed walk reaches
6 of 6 with two SQL queries and no model call.

## The nine that do not move, and why

All three remaining questions fail for one reason: **the estate passes URLs as
data**, so there is no shared literal to match.

- **Guest cart on login.** The client writes
  `endpoints.cartsUrl + "/" + custId + "/merge"` (`front-end/api/user/index.js:283`)
  and the server declares `@RequestMapping(path = "/carts")` plus
  `"/{customerId}/merge"` (`CartsController.java:15,35`). The two never spell the
  same string. A suffix rule would join them; it would also join a great deal
  else, and that is a measurement to run before believing, not a change to make
  on the strength of one case.
- **Address stored and read**, and **Rechnungsbetrag.** `orders` receives the
  address and item URLs inside the request body and dereferences whatever
  arrives. Nothing in `orders` names `user` or `catalogue` at all. No extraction
  rule can recover a link the code does not write down, and claiming otherwise
  would be inventing one.

That is the honest ceiling of this approach on this corpus: 20 of 29.

## What the review changed, and what it did not

A medium review of the branch found seven things. The four that touched a
number or a correctness claim:

- **Parked repositories were hop targets.** Neither the crossing nor the spread
  ceiling filtered `repo_state.enabled`, so `Reach` could cite a repository the
  operator had removed from the Repos page, and a token in two live plus two
  parked repositories was discarded as a convention. Both counted now, the same
  way `internal/ask` counts them.
- **The in-repo hop was not the rule it claimed to be.** It matched substrings,
  so the symbol `Item` hit `ItemsController` and `OrderItem`, and it counted
  definers per repository where `ask` counts them across the estate. It now
  tokenizes identifiers exactly as `ask` does. **The catalogue number is
  unchanged at 20/29 under the stricter rule**, which says the looser matching
  was not doing the work the figure was crediting it with.
- **The table would have stayed empty on every existing install.** An
  incremental poll passes only the paths a commit changed, so tokens would
  arrive one edited file at a time and a settled service would never get any.
  The migration now empties `last_sha` to force one full pass, the same remedy
  `0010_symbol_definitions.sql` uses for the same reason. Cheap: the embedding
  cache is keyed on content hash, so nothing is re-embedded.
- **Two false-destination shapes.** A bare `send` rule recorded `res.send("done")`
  as a queue called `done`, and a wildcard name rule recorded
  `exchangeRate = "USD"` as a queue called `USD`. Both are the kind of
  coincidence that produces a wrong edge between two services, which this
  package holds to be worse than a missing one. Fenced by tests.

Two performance findings were also real: the backward half of the in-repo hop
was an unconstrained join of every chunk against every symbol row, and the walk
had no fan-out cap. Both fixed, and the effect is visible — `TestFlowEdgeReach`
went from 16.0s to 0.68s.

## A rough edge worth naming

The in-repo hop that reaches `payment/service.go` goes **through
`component_test.go`**. It is a correct hop — the test does reference the service
— but a test file is a poor waypoint. Retrieval already demotes tests
(`TestDecay = 0.35`), and the same demotion belongs in this walk's ordering
before it feeds an answer.

## Reproducing

```
scratchpad/run-flow-eval.sh TestEvalIndex               # tokens are written by the indexer
scratchpad/run-flow-eval.sh TestFlowEdges              # boundaries, and the noise check
scratchpad/run-flow-eval.sh TestFlowEdgeReach          # 10/29, 14/29, 20/29
scratchpad/run-flow-eval.sh TestFlowComposedWalk       # the trail, end to end
```
