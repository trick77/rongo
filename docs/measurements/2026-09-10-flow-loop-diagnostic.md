# The loop stops when it thinks it is done, not when it runs out of turns

**Status: run on 2026-09-10 against the pinned Sock Shop corpus
(`2026-09-10-flow-corpus.md`), at six rounds and again at twelve. Settled: the
budget was not the limit. Two questions fail identically at both budgets, in
both arms, with rounds to spare.**

## The verdict, first

Doubling the budget moved the totals by two parts (21→23, 20→22) but moved the
*shape* a great deal:

- **Sequential chains are followable.** "What happens when a customer places an
  order?" went from 2/6 to **5/6 in both arms** — it crossed into payment and
  shipping once it had the turns. Cost: 77k and 83k tokens for that one question.
- **Fan-out is not.** "Welche anderen Dienste fragt der Bestelldienst ab" stayed
  at **2/5 in both arms at both budgets**, and "Wie wird der Rechnungsbetrag
  berechnet" stayed at **1/3 in both arms at both budgets** — the latter using
  six of its twelve rounds and stopping voluntarily.

The failure is not the budget. It is that the loop stops when it has a locally
plausible answer, without traversing an edge it has already been shown.

## Arms

`TestFlowLoopDiagnostic`, ten flow questions, four tools (rongo's hybrid search,
ripgrep, ctags symbols, read file), six rounds per question, both arms MiMo.

| arm | parts reached | wall clock |
|---|---|---|
| `mimo-v2.5-pro` | **21/30** | 440 s |
| `mimo-v2.5` | **20/30** | 268 s |

"Reached" means the file was SHOWN to the model by a tool result. Whether the
prose it then wrote was any good is a different question and is not scored here:
a weak answer over the right files is not a retrieval failure.

## What the trajectories show

Per-question detail survives for `mimo-v2.5` and for one `mimo-v2.5-pro`
question. The rest of the Pro detail was lost to a `tail` in the invocation, not
to the harness; the runner now tees. The totals above are unaffected.

The flagship question, `mimo-v2.5`, "What happens when a customer places an
order?" — **2 of 6 parts, budget exhausted without an answer**:

```
1. search("place order create order endpoint API")
2. read_file(front-end, api/orders/index.js, 1-130)
3. read_file(front-end, api/orders/index.js, 130-160)
4. read_file(orders, .../OrdersController.java, 1-200)
5. read_file(orders, .../OrdersController.java, 120-200)
6. read_file(front-end, public/js/client.js, 85-120)
never shown: payment/service.go, payment/transport.go,
             shipping/.../ShippingController.java,
             queue-master/.../ShippingTaskHandler.java
```

Step 4 put `OrdersController.java` in front of the model — the file that names
the payment URI and posts to shipping. The next two steps went back into the
front-end instead of following either. It is not that the flow was unfindable;
it is that the loop did not follow the edge it was already holding.

The rest of the `mimo-v2.5` arm:

| question | parts | rounds spent |
|---|---|---|
| customer places an order | 2/6 | exhausted, no answer |
| Wer wird benachrichtigt … Versand | 3/4 | exhausted, no answer |
| guest cart on login | 3/3 | answered |
| Zahlung abgelehnt | 2/2 | exhausted, no answer |
| who authorises a payment (control, one repo) | 1/1 | exhausted, no answer |
| Konto und Kartendaten | 2/2 | exhausted, no answer |
| address stored and read | 2/2 | exhausted, no answer |
| Welche Dienste fragt der Bestelldienst ab | 2/5 | answered |
| queue unavailable | 2/2 | answered |
| Rechnungsbetrag | 1/3 | answered |

Two patterns worth naming:

- **The queue edge was crossed.** "Wer wird benachrichtigt" reached
  `ShippingTaskHandler` in the consumer repository; only
  `ShippingConsumerConfiguration` was missed. The producer-to-consumer hop is
  not beyond the loop.
- **The multi-repo fan-out was not.** The two questions whose answer needs four
  or five repositories at once — order placement, and which services orders
  calls — are the two worst results, 2/6 and 2/5.

## Twelve rounds: the two questions that do not move

| arm | six rounds | twelve rounds |
|---|---|---|
| `mimo-v2.5-pro` | 21/30 | **23/30** |
| `mimo-v2.5` | 20/30 | **22/30** |

**"Wie wird der Rechnungsbetrag einer Bestellung berechnet?" — 1/3 in every arm
at every budget.** Both arms produce the same six-call shape and then stop, with
six rounds unspent:

```
1-3. search(Rechnungsbetrag / invoice amount / order calculation)
4.   read_file(orders, OrdersController.java)
5.   read_file(orders, entities/Item.java)
6.   read_file(orders, entities/CustomerOrder.java)
never shown: carts/.../ItemsController.java, catalogue/service.go
```

It finds `calculateTotal` — `quantity * unitPrice`, plus a hard-coded 4.99 — and
concludes. It never asks where `unitPrice` comes from. The answer it gives is
not wrong; it is confidently incomplete, which is the failure mode the corpus was
built to expose.

**"Welche anderen Dienste fragt der Bestelldienst ab?" — 2/5 in every arm at
every budget.** The Pro arm spends eleven calls, reads
`OrdersConfigurationProperties.java` (which names the payment URI and the
shipping URI) and `AsyncGetService.java`, and then goes back to the front-end and
greps a path. `payment/transport.go`, `shipping/…/ShippingController.java` and
`user/api/transport.go` are never opened. It had the edge in hand and did not
cross it.

## What this means for the graph

The graph is **load-bearing for fan-out and an optimisation for chains**:

- Where the flow is a chain, more turns get there. At 77-83k tokens per
  question — against a product that today spends one Pro call per question.
- Where the answer needs several destinations at once, more turns do not help,
  because the loop does not know it is missing anything. An edge table changes
  that: the far side of an edge arrives without the model having to decide to
  look for it.

The one part the order-placement question still misses at twelve rounds is
`queue-master/.../ShippingTaskHandler.java` — the far side of the
`shipping-task` queue, which is exactly what an integration edge would supply.

## What the six-round run could not tell apart

**Six of ten questions ran out of rounds without ever producing an answer**, the
single-repo control question among them. That control reaching 1/1 and still not
answering says the budget, not the corpus, ended those trajectories.

So the low part counts have two candidate causes and this run cannot separate
them:

1. the model cannot follow a cross-repository edge, which would make an edge
   table load-bearing; or
2. six rounds is simply too few to walk five repositories, which would make the
   edge table an optimisation — the thing it was proposed as.

The order-placement trajectory hints at (1): the edge was in hand at step 4 and
not taken. One trajectory is not evidence.

**That is what the twelve-round arm above settled**: cause 2 is real for
sequential chains and cause 1 is real for fan-out. Both were true at once, which
is why the aggregate barely moved while the individual questions moved a lot.

A second limitation, already recorded in the corpus document and repeated here
because this is where it bites: both arms are MiMo. The successes are
unambiguous - a chain followed to 5/6 is a chain followed. The two stuck
questions are not: "the loop stops early on fan-out" and "this model family
stops early on fan-out" produce identical numbers here, and only a non-MiMo arm
separates them. The graph helps in either case, which is why this was not worth
another arm; a claim about agentic retrieval IN GENERAL would be.

## Correction to the specification

The review asked for "a budget of six calls". This measured six **rounds**: a
model may issue several tool calls in one assistant turn, and questions here
spent up to 22 calls inside six rounds. Rounds is the more useful budget — it
counts how many times the model got to think — but it is not what was asked for,
and the numbers above should be read as rounds everywhere.

## Reproducing

```
scratchpad/run-flow-eval.sh TestEvalIndex          # once, builds the corpus
scratchpad/run-flow-eval.sh TestFlowLoopDiagnostic # the arm
BACKEND_FLOW_BUDGET=12 scratchpad/run-flow-eval.sh TestFlowLoopDiagnostic
```

Not `hack/run-eval.sh`: it hard-overrides `BACKEND_EVAL_DB` and
`BACKEND_REPO_ROOT` to the Go corpus's paths, and one database holding two
corpora measures neither.
