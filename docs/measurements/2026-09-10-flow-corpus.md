# A corpus that can show the problem: Sock Shop, pinned

**Status: corpus pinned 2026-09-10, ten flow questions written, nothing measured
yet. This document exists so the next arm has a corpus to name.**

Every measurement this project has published was taken on rongo, peeq and
go-sqlite3: Go, English, one message queue between none of them. Two review
rounds argued the index is shallow for flow questions — "what happens to a
customer record on close" is an edge question, and edges between services are
exactly what a corpus of three Go libraries does not have. The argument could
not be settled, in either direction, on a corpus with no edges in it.

## What was required, and what was rejected

Three criteria, all checked by cloning and reading, not by GitHub code search —
`/search/code` returned `total_count: 0` for `kafka` in a repository and for
`rabbit` in four others where the code is demonstrably present, so its zeroes
carry no information:

1. separate repositories, not modules in one;
2. a message producer and its consumer in different repositories, on the same
   named destination;
3. a REST client whose target route is served by a different repository.

**`spring-petclinic-microservices` fails all three.** One repository containing
`spring-petclinic-customers-service/`, `-vets-service/`, `-visits-service/` and
five more as directories, Spring Cloud, REST only, no queue.

**No multi-repo Kafka estate was found.** `flowing-retail`,
`piomin/sample-spring-kafka-microservices` and the other well-known Kafka
samples are single repositories with modules, which loses the cross-repo half.
Sock Shop's queue is RabbitMQ instead. The edge class is the same — a named
destination, a producer, a consumer — and the extraction rule differs only in
which annotation and method it matches, so the Kafka rules will ship unmeasured
until a corpus for them exists. That is recorded here rather than glossed.

## The corpus

`microservices-demo`, eight repositories, local clones under
`/tmp/sockshop-pin/<name>`, each with a branch `pin20260910` at:

| repository | sha | language |
|---|---|---|
| front-end | `52dee65` | Node |
| orders | `546a10c` | Java |
| carts | `f4e8005` | Java |
| catalogue | `925e08e` | Go |
| payment | `384e334` | Go |
| shipping | `9c0fbfa` | Java |
| queue-master | `7dc3372` | Java |
| user | `e1a79e7` | Go |

Manifest: `repos.yaml` in the `flow-corpus` worktree, one project of eight, which
loads through `repos.Load` (verified). It declares **no `part`, no `description`
and no `uses`**, on purpose: all three are hand-written facts that reach routing
and the answer prompt, and declaring them would hand the model the cross-repo
structure the questions exist to test. An arm about the manifest can add them.

## The three edges the questions turn on

Each is a bare string literal appearing in two repositories with no import, no
shared type and no symbol in common — invisible to `ctags`, and not something an
embedding of the question lands on:

| destination | producer / client | consumer / server |
|---|---|---|
| `shipping-task` | `shipping/…/RabbitMqConfiguration.java:18`, sent at `ShippingController.java:40` | `queue-master/…/ShippingConsumerConfiguration.java:15`, handled at `ShippingTaskHandler.java:13` |
| `/paymentAuth` | `orders/…/OrdersConfigurationProperties.java:12` (Java) | `payment/transport.go:29` (Go) |
| `/orders` | `front-end/api/orders/index.js:122` (Node) | `orders/…/OrdersController.java:50` (Java) |

The order flow crosses five repositories and three languages in one path:
front-end → orders → (user, catalogue) → payment → shipping → queue.

## The questions

Ten, in `backend/internal/retrieve/eval/flow-questions.json`, five German and
five English, in the shape `questions.json` already uses. Nine are
`composition` — parts of one mechanism, where asking the reader which repository
they meant is the wrong move — and one is a single-repo `unique` control
question, so that a flow miss can be told apart from a retrieval problem that has
nothing to do with edges.

Every candidate carries a `verified` string with file and line, read from the
pinned code rather than assumed.

Two questions are worth naming because they are what the reviewers were arguing
about:

- *"Welche anderen Dienste fragt der Bestelldienst ab, bevor eine Bestellung
  gespeichert wird?"* — the repo-narrowing prompt cannot help here, because
  naming the repositories **is** the answer.
- *"What happens to a shipment when the message queue is unavailable?"* — the
  send is wrapped in a `try/catch` that returns the shipment anyway
  (`ShippingController.java:41-44`), so the loss is silent and invisible from
  either repository alone.

## The index

`TestEvalIndex` against this manifest, into its own database
(`/tmp/rongo-flow.db`) and its own repository root (`/tmp/rongo-flow-repos`),
never the Go corpus's, because one database holding two corpora measures
neither. 482 files, 3517 chunks, 128 s:

| repo | files | chunks |
|---|---|---|
| front-end | 147 | 2146 |
| user | 46 | 349 |
| orders | 56 | 334 |
| carts | 63 | 252 |
| catalogue | 64 | 136 |
| queue-master | 38 | 108 |
| shipping | 34 | 98 |
| payment | 34 | 94 |

The 180 MB of the two largest repositories is images, skipped as binary; the
code is a few megabytes.

## The diagnostic, and what it cannot settle

`TestFlowLoopDiagnostic` in `backend/internal/retrieve/eval/flowloop_test.go`
gives the model the capabilities rongo already has — hybrid search, ripgrep,
ctags symbols, read file — with a budget of six tool calls, and reports the
**trajectory** per question: which call lost the thread, and which candidate
parts were never shown. It scores what the model was SHOWN, not the prose it
then wrote: a poor answer built on the right files is not a retrieval failure.

It runs out of band, with its own minimal tool-calling client, because
`backend/internal/llm/client.go:6` says "No tools, no image path" and a
diagnostic must not put an unused code path into the shipping client.

**Both arms are MiMo** — `mimo-v2.5-pro` and `mimo-v2.5`. A frontier arm was
considered and deliberately dropped. The consequence has to be stated wherever
the result is quoted: if BOTH arms fail, this measurement cannot separate
"agentic search does not work for this problem" from "this model family cannot
hold a six-call trajectory". Only a success is unambiguous.
