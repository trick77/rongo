# What a repository is built from, read from its own manifests

**Status: built 2026-09-11. Units from nx, Maven, Gradle and Go manifests;
Maven and npm-style coordinates in `repo_deps`; the module cut follows
declared units; two units that use each other compose; the answer prompt is
told what a narrowed repository is built from. Route extraction composes a
class-level prefix and reads a generated client's template. On the flow
corpus the product path moves from 26/30 parts to 27/30 and the flagship
flow to 5/6. Route suffix matching was measured and stays off.**

## Why

The analyst's project is two repositories: an nx workspace with three
Angular applications and four libraries, and a Maven parent with four
Spring Boot services and three libraries. rongo saw two things: `repos.yaml`
named the repositories, the directory cut in `internal/modules` offered
`apps/vorerfassung/src/app`, and `repodeps` read go.mod alone. None of that
is what a person means by "the Vorerfassung" or "the intranet service".

Everything below is read from a build manifest and replaced with its
repository on every index run. No documentation is read, nothing is
inferred from names, no model runs — the same rule as `integration_tokens`,
for the same reason: a fact that can go stale on its own is a fact the
answer will one day state with confidence.

## What was built

`internal/units` reads:

| manifest | unit | edges |
|---|---|---|
| nx `project.json` | app or library, name, tags | `implicitDependencies`; imports through `tsconfig.base.json` aliases, read out of the indexed chunks after the file pass |
| Maven `pom.xml` (packaging not `pom`) | service if the boot plugin is present, else library; artifactId | sibling artifactIds → unit edge; anything else → `groupId:artifactId` coordinate |
| Gradle `settings.gradle(.kts)` + `build.gradle(.kts)` | service if the boot plugin is applied, else library | `project(":x")` → unit edge; `"g:a:v"` → coordinate |
| `go.mod` below the root | Go module | (coordinates were already in `repo_deps`) |

A build at the root ("." — a single-module pom) is the repository, not a
part: its coordinates reach `repo_deps`, and no unit is stored.

Where it lands (`0016_units.sql`: `units`, `unit_deps`; one forced
re-index):

- **The module cut.** `modules.Cluster` emits one module per declared unit,
  whatever its size, and runs the directory rule over what no unit claims.
  A card built over schadenmeldung-ui offers `claims`, `vorerfassung`,
  `bagord`, not their `src/app` directories. Measured only by unit test: the
  analyst's repositories are private and not in any pinned corpus.
- **Composition inside a repository.** `Router.anyDependency` reads
  `unit_deps` for two candidates of one repository: an app and the library
  it imports are one mechanism, no card. Two directories that are not units
  fall through to the margin and the judge as before.
- **The answer prompt.** `describeProjects` appends a units paragraph per
  narrowed repository ("Repository X is built from 7 parts … intranet-service
  uses persistence, rimex, uebersichtsconverter … also depends on
  camunda-intranet, which are outside this repository"), closed by the same
  configuration sentence as the project block.
- **Cross-repository edges from Maven and npm.** `repodeps.SyncWith` takes
  the units' coordinates beside go.mod, so a Java repository that pulls what
  another publishes is a manifest edge for routing.

## Route extraction, and what the flow corpus says about it

Two rules arrived with this:

- **Class-level prefix composition.** Spring writes `@RequestMapping("/api")`
  on the class and `@GetMapping("/schadenfall")` on the method. Both the bare
  method path and the served path `/api/schadenfall` are recorded, and the
  class-level path stays a route of its own ("/carts" is what the front-end
  calls). The first draft dropped the class-level token; the flow corpus
  caught it at once — 25/30 against the 26/30 the previous document
  published — and the token table showed `/carts` gone from
  `CartsController`.
- **Template literals.** An OpenAPI-generated Angular client writes
  `${this.configuration.basePath}/beruf`. The base is configuration; the
  constant after it, cut at the next interpolation, is the route.

`TestFlowGathered`, flow corpus re-indexed under the new rules (58 tokens
against 55; the eleven shared tokens are the same eleven):

| arm | parts | questions whole | mean sources |
|---|---|---|---|
| search only | 15/30 | 3/10 | 20.0 |
| symbol walk | 20/30 | 5/10 | 158.6 |
| **symbol walk + crossings (the product)** | **27/30** | **7/10** | 164.4 |
| symbol walk + crossings, route suffix matching | 25/30 | 5/10 | 169.9 |

The product arm gains one part over `2026-09-11-edges-in-gather.md`: the
order-placement flow reaches `payment/service.go` at hop 3, one symbol hop
past the crossing that lands on `payment/transport.go`, and stands at 5/6.
Only `ShippingTaskHandler` is still out: the crossing on `shipping-task`
lands on the far side's configuration and the budget is spent before the
handler.

**Suffix matching stays off.** The rule that was supposed to join
`"/merge"` to `/carts/{customerId}/merge` costs two parts and two whole
questions: loose route matches cross earlier and eat the reserve, and the
guest-cart question it was written for was already 3/3 through the product
path. `edges.Match{Suffix: true}` stays in the code with this table as the
reason, the way `RepoDecay` does.

## What this does not settle

- No measurement over an nx or Maven multi-module corpus exists: Sock Shop
  is one build per repository, so `units` is empty there and the cut,
  the composition rung and the prompt paragraph are covered by unit tests
  over manifest fixtures shaped like the analyst's. The schadenmeldung
  question set of the answer-quality harness is where those get a number.
- The UI-to-service edge of the analyst's project is still not a token
  edge: the client's route is `/beruf`, the server's is `/api/beruf` under a
  class prefix, and exact matching does not join them. The composed server
  path `/api/beruf` is recorded now; a client that spells the whole path
  matches it. A client spelling only the tail does not, and suffix matching
  is measured as a loss.

## Reproducing

```
sqlite3 /tmp/rongo-flow.db "UPDATE repo_state SET last_sha=''"   # the migration does this once on a real install
hack/run-flow-eval.sh 'TestEvalIndex$'
hack/run-flow-eval.sh 'TestFlowGathered$'
hack/run-flow-eval.sh TestFlowEdges
```
