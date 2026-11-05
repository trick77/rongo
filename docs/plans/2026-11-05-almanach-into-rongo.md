# Almanach into Rongo

Status: plan, 2026-11-05. Nothing built. Phase 0 decides whether anything after it happens.

## Why

Rongo answers questions from code. Its weakest questions are the cross-repository
flows: the Sock Shop corpus was pinned because "what happens when a customer places
an order" spans five repositories and three languages, and nothing but string
literals connects them (`docs/measurements/2026-09-10-flow-corpus.md`). The flow
manifest declares no `part`, `description` or `uses` on purpose: hand-written
structure would hand the model what the questions test.

Almanach writes exactly that structure, from code, by model: a page per repository
(components, interfaces, flows, invariants, decisions, pitfalls, each with its files)
and a page per project describing how the repositories wire together. The thesis of
the merge is that those pages, read as context, lift Rongo's answers on flow
questions. Rongo's AGENTS.md forbids storing model text about code without a
`TestEvalMeasureAnswers` number. So the number comes first, and it is the whole
reason for the integration. No number, no merge.

Decided with the user (2026-11-05): start from an empty database, no Almanach data
carried over; wiki is display-first in the product and enters retrieval only on the
eval's say; mimo-v2.6-pro is the only model allowed for this work, key
`LLMWIRE_MIMO_API_KEY` from `../ai-keys`; every eval invocation finishes inside 90
minutes; English chrome, German stored content; Rongo hosts (shell A of
`docs/plans/2026-11-05-almanach-integration-mock.html`).

## Phase 0: the number

One question: do Almanach's pages, in the doc lane, move `TestEvalMeasureAnswers` on
the flow corpus? Two arms inside ONE database, two runs each, judged by the same
model.

### 0.1 Produce the pages (prep, outside the clock)

Almanach as it stands today, nothing merged yet. Its prompt eval
(`backend/internal/consolidate/prompteval_test.go`, `TestPromptEval`) runs full
baseline runs over `EVAL_REPOS` with `EVAL_MODEL=mimo-v2.6-pro` and
`EVAL_KEY_VAR=LLMWIRE_MIMO_API_KEY` by default, one project, timeout 3h, and
`TestEvalLoad` writes the resulting pages into a SQLite file (`EVAL_DB`).

- `EVAL_REPOS` = the eight pinned local clones under `/tmp/sockshop-pin/<name>` on
  branch `pin20260910`, project key `SOCKSHOP`. Check first that `TestPromptEval`
  clones a local path (git does; the test may validate a URL shape, then a
  `file://` URL or a one-line relaxation in the test).
- Budget: `BACKEND_BASELINE_MAX_CALLS` default 16 per repository, 8 repositories, so
  at most 128 calls plus the project step. Expect one to two hours and tens of
  dollars at pro's rate. It is prep, not the measurement.
- The eval's timeout is 3h and the project step runs LAST, so a cut-off loses the
  page this experiment most wants. Run `front-end` alone first, time it, then set
  the timeout for the eight from that; or run the repositories in two batches of
  four into the same `EVAL_DB` if the harness allows it, the project step after
  the second.
- Output wanted: the pages as Rongo-readable files. Almanach's Markdown export
  (`backend/internal/api/markdown.go`) is an HTTP route; rather than serving the
  eval database, add one harness test beside `TestEvalLoad`,
  `TestEvalExportTree`, that reads `EVAL_DB` and writes a tree:

      <out>/<slug>/ueberblick.md               the repository summary
      <out>/<slug>/<kind>/<id>-<name>.md       one file per statement
      <out>/project/ueberblick.md              the project summary
      <out>/project/<kind>/<id>-<name>.md      one file per project statement

  Each statement file: `# <name>` (kind in an eyebrow line), the text as stored
  (`markup` → Markdown as the export does), a `Files:` line with the statement's
  files prefixed by `<slug>/`, a `PR:` line with its sources. German, Swiss spelling,
  as stored. ~80 lines of Go in almanach's eval, harness only.

**Decision: one document per statement, not one page per repository.** A statement
is the unit an answer can cite and a reader can open; a 12 000-character page would
be cut into ~600-token line windows that split statements mid-text and would win
fusion as one dense prose blob. The summary is one document on its own.

### 0.2 Put the pages into the corpus without touching the index code

Rongo already has the mechanism: a **snapshot repository** (`repos.yaml` entry with
no `clone_url`; `gitrepo.EnsureSnapshot` inits, adds and commits the directory on a
branch `snapshot`; indexed by the normal pipeline; the directory is never deleted).

- Copy the tree from 0.1 to `$BACKEND_REPO_ROOT/sock-shop-wiki/`.
- `flow-repos.yaml` gains a ninth member of project `sock-shop`:
  `- name: sock-shop-wiki` with `snapshot: true`.
- `scripts/measure.sh flow TestEvalIndex` indexes it: `TestEvalIndex` already takes
  the snapshot path (`snapshotHead` → `gitrepo.EnsureSnapshot`, `eval_test.go:329`),
  so the directory is committed and indexed like the poller would. Every file is
  `.md`, so `retrieve.IsProseDoc` holds and the doc decay (0.7,
  `2026-09-05-doc-demotion.md`) applies: the wiki is a document, demoted like any
  document, never a filter.
- The eight code repositories are untouched: same shas, same chunks, same vectors.
- **Embeddings are the build's constant** (`embed.Model = text-embedding-3-small`,
  OpenAI key), not a model choice: indexing the wiki repository embeds its chunks
  through that endpoint. Said here so it can be objected to before anything is
  spent; the pro-only rule covers the chat lanes and the judge.

**Why a ninth repository and not the pages under each code repository** (the shape
phase 2 ships): committing pages into the code clones moves the pinned shas, and
every later arm would measure a different corpus; a wiki branch per clone
(`pin20260910-wiki`) is the alternative if the ninth repository turns out to confound
(see 0.3). What a ninth member changes is checked: a project turn is ONE fused
search over its members with no per-repository share (`ask/pipeline.go`
`searchScoped`, `oneProduct`), `RepoDecay` ships off
(`2026-08-22-repo-diversity.md`), and gather caps nothing per repository. The
ninth member is the same in both arms (0.3), so what it adds to the prompt's
member list is not an arm difference.

### 0.3 Two arms, one database

The off arm must not see the wiki, the on arm must, and the index must not be
rebuilt between them (`2026-09-16-stable-order.md`: two fresh indexes differ by
embedding drift).

- The switch is **inside retrieval and harness-only**: `BACKEND_EVAL_WIKI=0` makes
  `evalRetriever` wrap the retriever so every lane drops hits of repository
  `sock-shop-wiki` BEFORE fusion (the off arm still gets a full K of code hits);
  `1` is the on arm. The wiki repository stays enabled in both arms, so
  `projects.Load`, the routing scope, the candidate fold and the answer prompt's
  member list are identical across arms and only retrieval differs. Parking the
  repository (`enabled: false`) was considered and rejected: it changes scope and
  prompt as well, and `SyncSpecs` runs at boot, not inside the test.
- Check before the paid run (one test, no model): with `BACKEND_EVAL_WIKI=0` a query
  only the wiki matches returns nothing; with `1` it returns `sock-shop-wiki` hits
  with the doc decay applied.
- Models: `BACKEND_LLM_MODEL` and `BACKEND_LLM_GATE_MODEL` = mimo-v2.6-pro,
  `BACKEND_EVAL_ANSWER_MODEL`/`_GATE_MODEL` unset. The judge reads
  `BACKEND_LLM_MODEL`, so it is pro too. AGENTS.md pins the judge to mimo-v2.6-flash
  because every recorded number was graded by it; with pro the two arms compare
  against each other, not against the record. The measurement document says so.
- Audience BA (the default), reranker on, locate loop off (as the harness runs it),
  processes on. Nothing else varied.

### 0.4 The clock

`scripts/measure.sh` runs `go test -timeout 90m`. One invocation = one arm:

| what | on record | this plan |
|---|---|---|
| 2 runs × 12 questions, Opus | 21 min | — |
| 2 runs × 12 questions, mimo-flash | 60 min | — |
| 1 run × 12 questions, mimo-pro | — | one invocation, `BACKEND_EVAL_ANSWER_RUNS=1` |

Pro is the larger model and flash already took 60 minutes for two runs, so the
plan does not bet a paid run on fitting two: **four invocations of one run each**
(off, off, on, on), `BACKEND_EVAL_WIKI` set per invocation, each answers file kept.
Same cost as two double runs, every invocation safely inside the 90-minute
timeout, "run twice" satisfied by two invocations per arm. The off arm runs first
and its baseline is read before the on arm starts. Index build and the Almanach
describe runs are outside the clock.

### 0.5 Reading the number

`TestEvalMeasureAnswers` reports per run: rubric claims present, contradicted,
forbidden asserted, cited parts, asked, failed, tokens. Record all of it in
`docs/measurements/2026-11-xx-wiki-context.md`, both arms, both runs, plus the
per-question table for the eight composition questions.

**Pre-registered reading.** The record sits at 37–38 of 41 rubric claims present on
this corpus, so the whole-set number has three or four claims of headroom against
noise of one and may not move measurably. The go/no-go is therefore read on
subsets, fixed here before the run:

- the eight composition questions (the thesis: cross-repository wiring);
- the EN and DE questions separately (the pages are German; a lift on DE alone
  says language, not thesis, and the next step is then an English restyle of the
  pages in Almanach, not a no-go);
- per question, in the per-question table, for both arms and both runs.

Go if, in BOTH on-arm runs against BOTH off-arm runs: rubric present on the
composition subset is higher, contradicted and forbidden do not rise, asked does
not rise. One-question gaps are noise on this corpus
(`2026-10-26-opus-both-lanes.md` records one forbidden claim per arm as common);
the bar is a gap in the same direction twice. **Cited parts is counted, not
excused**: a fall there means wiki hits displaced code hits in the context, which
is exactly the failure the invariant guards against. It is reported as the trade
(claims gained against code parts lost), and the user judges it.

No-go: write the document, keep the mock, stop. Second experiment only if the user
asks: the project page alone as answer-prompt context (no retrieval), which tests the
wiring without the demotion.

### 0.6 Side readings the run gives for free

- Are wiki hits cited at all, and how often is a turn `DocsOnly`? If the wiki wins
  fusion so hard that code leaves the context, the decay for wiki paths needs its
  own sweep before phase 2.
- German pages, English questions: the cross-language hits come from the vector
  lane only. Noted for phase 2 (an English wiki is a prompt-epoch restyle in
  Almanach, not a UI switch).

## Phase 1: the wiki inside Rongo (only on go)

Rongo hosts. Almanach's writer moves into Rongo as `backend/internal/wiki/...`; the
clone side of Almanach does not move: Rongo already keeps checkouts.

### What moves, what merges, what is dropped

| Almanach package | fate |
|---|---|
| `diagram` (379) | moves, `internal/diagram`. Rongo has no Go IR; the UI's `diagramIR.ts` is the counterpart. |
| `markup` (635), `lang` (95) | move. `lang.SwissSS` merges with `ask/swiss.go` (one spelling function). |
| `redact` | merge into Rongo's `internal/redact` (71 diff lines in the core; add `File`, `SecretKey`, `prose.go` `Text`/`JSON`; keep every test from both). |
| `baseline` (1779, pure) | moves to `internal/wiki/baseline`; its `Code`/`gitrepo` types map onto Rongo's `gitrepo.Client`/`Reader`. |
| `consolidate` (5045) | moves to `internal/wiki` as the worker; `clone_busy` goes (Rongo's poller and the worker serialize on the DB, as the indexer does). |
| `inference` (1157) | moves to `internal/wiki/inference`. Decision: it keeps calling llmwire directly with strict JSON schema, through Rongo's lane config: describe/merge/restyle on the **answer lane** (a human reads the page), catalogue on the **gate lane**. Rongo's `llm` package gains nothing yet; a later cleanup may add `WithJSONSchema` and fold it. No reasoning level in code (`BACKEND_MODEL_REASONING_EFFORT` is dropped; `BACKEND_LLM_REASONING` applies). |
| `prompts` (10 txt, 80 kB) | move, `go:embed`, under `internal/wiki/prompts`. First embedded prompts in Rongo; AGENTS.md note. |
| `ingest`, `teamkeys` | move: the noergler webhook is the delta feed and stays. `POST /webhooks/noergler`, bearer only, public route in `httpapi.routes()`. Rongo's "no webhooks" comment is about index triggers; this one triggers no index. |
| `manifest` (1714) | NOT moved in phase 1. Rongo's `repodeps`+`units` already read go.mod, Maven, npm. The prompts' `Shared`/`InnerEdges` read from `repo_deps`. Gradle is a gap, listed. |
| `store` (4241) | moves as `internal/wiki/store` minus `sources.go`, `cloneurl.go`, `deactivate.go`, `manifests.go` and the listing half of `baseline.go`. One squashed migration `0041_wiki.sql` in Rongo's runner (start-empty decision makes squashing legal). Table names kept (none collide with Rongo's); `source_repos` becomes a view-like read of `repo_state` where the worker needs clone state. |
| `clone`, `bitbucket`, `secret`, `urlorigin`, `authroute`, `auth`, `config`, `httpapi`, `cmd` | dropped. Rongo's poller, auth (kit, five modes), config, server. |
| `api` (3189) | handlers move into `httpapi` as `wiki_*.go` behind a new `Deps.Wiki` interface (nil = 503): projects, repo page, note, problems, queue, dependencies, Markdown exports. Sources routes dropped. |

Sizes: ~14 k Go lines move, ~9 k are dropped or replaced, tests follow their code.
Coverage gate 75% on changed lines holds because the tests move too.

### Wiring

- Trigger: a baseline run is queued when an index run succeeds
  (`Poller.indexAndMark`'s `between` hook): `first_ready` on the first index,
  reconcile when the documented commit differs and `BACKEND_WIKI_RECONCILE` has
  passed, retry/epoch as today. The run pins `repo_state.last_sha` and reads the
  checkout at that sha through `gitrepo.NewReader`.
- Worker goroutine beside `poller.Run` in `main.go`; same `pollCtx`, `workers.Add`.
- Config, all new, `BACKEND_WIKI_*`: `TEAM_KEYS_PATH`, `MAX_CALLS` (16),
  `MAX_BYTES` (4 MB), `RECONCILE` (720h), `DIAGRAM_MAX_NODES/EDGES/PER_PAGE`,
  `PROBLEM_WINDOW_DAYS` (90), `ENABLED` (true). Almanach's `BACKEND_MODEL` family is
  gone: models are Rongo's lanes.
- sqlite: Rongo's locked pair (ncruces v0.23.3 + sqlite-vec) wins; Almanach's store
  is on v0.36.0, so compile and run its store tests on the older driver first
  (json1 and partial indexes are old; expect no issue, verify).
- Repos stay `repos.yaml` in phase 1. Project = Rongo's project; Almanach's
  `projects`/`repos` tables are fed from `repo_state` (name, project), not from a
  listing. A repository that leaves the file leaves the wiki (departure run).

## Phase 2: pages in retrieval, productized

What phase 0 measured was a snapshot repository of Markdown files. The product
form, same shape, no new lane:

- When a revision is written, the worker writes one document per statement and one
  for the summary into `files`/`chunks`/`chunks_vec`/`chunks_fts` through
  `indexer.Writer.ReplaceFile`, under the statement's repository, path
  `.wiki/<kind>/<id>.md`, sha = the revision id. A dropped statement deletes its
  file. Embedding goes through `embed.Cache` like any chunk.
- `retrieve.IsDocPath` already matches `.md`; a `.wiki/` prefix check gives the
  hit `Kind: wiki` so the UI cites it as the mock draws it (ochre marker, opens the
  statement, not a file) and `sourceview` refuses it.
- **Landmine, named now:** a full index run calls `DeleteFilesNotIn` against the
  git tree (`indexer/index.go:130`) and every boot runs `Indexer.Sweep`; both would
  wipe synthetic `.wiki/` files that are in no tree. Both must exempt the prefix
  (one `keep` rule, one test each), or the documents live in their own table and
  only their chunks join `chunks`. Decided when phase 2 starts; the first is the
  smaller change.
- The project page's statements attach to the repository their first file names,
  else to every member (one document each, deduplicated by content hash in the
  embed cache).
- Re-run phase 0's arms inside Rongo (wiki worker on, documents written by the
  product path) and record parity with the snapshot measurement before this ships.
- AGENTS.md: the "never store or embed model text about code" invariant is
  rewritten to name the measurement and the one allowed form (a statement with its
  files, under the doc decay, cited apart).

## Phase 3: UI (shell A of the mock)

- Rail: Ask · Projects · Dependencies · Queue (Sources only with phase 4). SPA
  allowlist in `backend/web/embed.go` and `ui/src/routing.ts` grow together.
- Pages ported from almanach `ui/src`: ProjectsPage, ProjectPage, RepoPage,
  NotePage, QueuePage, DepsPage, Rail, RepoSwitcher, Description, Problems,
  NotesTable, CompactMap, mapLayout, Diagram. The five `diagram*.ts` copies collapse
  onto Rongo's originals (almanach's 13-line export change to `diagramLayout.ts`
  is applied there).
- `strings.ts` (733 lines, German) becomes English UI strings in Rongo's style
  (hardcoded JSX); kind labels, zone titles and stored content stay German as
  Almanach writes them. Swiss by string function, never prompt.
- Projects page: Almanach's project page replaces `RepoList`'s panel; Rongo's index
  columns join its repositories table (mock §2).
- Ask: wiki citation kind in the Sources pane, "Open in wiki", statement "Ask about
  this" as a paste (`PasteChip`, `pastes.ts`), project teaser and shape pill on the
  clarify card (`Candidate` gains `Teaser`, `Shape` from the project page).
- Every visible change driven in the running app with Playwright before merge.

## Phase 4 (separate plan, not started): sources replace repos.yaml

Almanach's `clone` poller, `sources` store, Bitbucket listing, token sealing and the
Sources pages. It overturns Rongo's "Repos page is read-only, list in repos.yaml" rule
and merges two git drivers. The mock shows the target; the plan is written when
phases 1–3 are in.

## Open decisions (recommendation first)

1. **Judge model**: pro (user's constraint) vs flash (AGENTS.md). Pro for this
   measurement, numbers compared within it; the document says the record is not
   comparable. Alternative: ask for a flash exception for the judge only.
2. **Project page statements in phase 0**: under `project/` in the snapshot repo,
   so they are cited as `sock-shop-wiki/project/...`. Alternative: duplicate them
   under every repository; rejected (duplicate hits distort fusion).
2a. **Ninth repository vs wiki branches per clone** (phase 0 shape): ninth
   repository, zero index code touched and the shas stay pinned; the retrieval
   switch keeps both arms on one scope. Wiki branches per clone are the fallback
   if 0.6 shows the ninth member distorting gather, and they match phase 2's
   shape exactly.
3. **inference stays on llmwire directly** (phase 1) vs folding into `llm` first.
   Direct first: the merge is large enough; folding is a cleanup with its own PR.
4. **Gradle manifests**: dropped in phase 1, Rongo's `units` covers Maven/npm/go.
   Port `manifest/gradle.go` only when a Gradle estate is indexed.
5. **Almanach repository**: archived after phase 3 ships; `diecibaerg-almanach-infra`
   retired with it. noergler keeps posting to the same webhook path on Rongo's host.

## Verification per phase

- Phase 0: the measurement document with both arms × two runs and the go/no-go
  line; `TestCohortHasADocLedAxis`-style guard not needed (harness only).
- Phase 1: `make check` green; a fixture repository (local `git init`, as almanach's
  tests do) indexed then described against an `httptest` model fake; no test hits
  a real model. Queue page shows the run; Markdown export matches almanach's golden
  tests moved over.
- Phase 2: the phase 0 arms re-run inside Rongo, parity recorded.
- Phase 3: Playwright drive of every page in shell A; screenshots next to the mock.
