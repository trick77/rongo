# rongo

Rules, not description. The code is truth — how a rule is implemented is discoverable, so it is not written here. Reasoning behind a number lives in `docs/measurements/`; a bullet citing one → read it before overturning the rule.

## Conventions
- English throughout: docs, specs, comments, UI copy, prompts, answers.
- Feature branch per phase (`feat/phase-N-...`). Never commit to `master`.
- TDD: failing test first, then the smallest implementation.
- `.yaml` never `.yml`. `Containerfile` never `Dockerfile`.
- No test hits a real LLM, embeddings endpoint or git remote. `httptest` fakes, fixture repo built locally with `git`.
- Coverage floor 75% both sides, plus 75% on a PR's changed lines. `make coverage`; CI gates both. `hack/coverage-*` and `hack/strip-comment-lines.go` are byte-identical with ../peeq — fix in the family, never fork.
- Tests need `-race` (cgo). The binary stays `CGO_ENABLED=0`.

## Locked choices (do not change without agreement)
- Pure-Go SQLite: `ncruces/go-sqlite3` + `asg017/sqlite-vec-go-bindings/ncruces` (wasm) + FTS5.
- **Those two are one unit** — vec is wasm compiled against ncruces' SQLite, so skew breaks vector search at runtime, not the build. Bump both or neither, never a lone Dependabot PR. Same pair as peeq/loom (`v0.23.3` / `v0.1.7-alpha.2`) so fixes transfer; read the alpha's changelog first.
- A test drives `vec0` end to end — create, insert, KNN, assert the neighbour — so an incompatible bump fails CI rather than production.
- One SQLite file is the whole datastore. No Postgres, Redis, vector service.
- stdlib `net/http`. No framework, ORM or router.
- **No tree-sitter** (cgo). `ctags` covers ~150 languages; where it yields nothing, the line window is the normal path.
- Runtime image stays non-distroless: rongo shells out to `git`, `rg`, `ctags`.
- **`ctags` must be universal-ctags** — macOS ships BSD ctags at `/usr/bin/ctags`, and the wrong one yields an empty symbol index rather than an error. Verify at startup, fail loudly.
- New config → a `BACKEND_*` env var, nowhere else.

## Models
- Two MiMo deployments, hardcoded in `internal/llm/client.go`, never env vars.
- **Pro** where a human reads: the answer. **non-Pro + `ShortGate`** everywhere else. The bar is "output is an id or a label", not "doesn't think" — both deployments reason, and `WithoutThinking`, `ShortGate` and `WithTemperature` are separate switches. Don't couple them.
- **Routing judge is non-Pro**, after two measurements that went opposite ways. Loom corpus: Pro 48/61+50/61 vs non-Pro 42/61+43/61 (`2026-08-19-candidates.md`). Pinned 2026-08-20 corpus, 65 questions, twice: ShortGate 47/65 twice, Pro 47/65 then 46/65 (`2026-09-06-routing-rerun.md`). Don't move it back without a corpus and a number.
- **Pin `WithTemperature(gateTemperature)` on every call returning an id, label or decision.** Unpinned, the judge re-rolled 3 of 61 per run. The answer call stays unpinned; a person reads it.
- **Pinning narrows the re-roll, it does not remove it** — two pinned runs still differ by one question. Never conclude anything from a one-question gap; run it twice.
- **Never rank routing settings by accuracy.** 49 of 65 questions want no card, so never-asking scores 0.754 and accuracy drifts every threshold toward silence. Price the errors apart: needless card = 1, missed ambiguity = W. The shipping ladder is cheapest from W = 2 up and beats never-asking above W = 1.14 (`2026-09-06-routing-cost-metric.md`).
- Cap every call with `WithMaxTokens` unless truncation would be worse than length.
- BA answer = core mechanism, three to five paragraphs, then stop. Edge cases go to a follow-up. DEV gets room for inline code.
- **Every answer opens with ONE sentence that answers the question**, then explains - both audiences. A list only where the mechanism really is a set (branches, options, ordered steps); prose that is prose stays prose, and a marker sits on the item making the claim. No headings: mocked and left out, because a short answer wearing three of them looks over-built and the model misjudges that more often than it misjudges a list.
- Embeddings cached by chunk content hash; never re-embed unchanged content.
- Prices from models.dev, never typed — there is no `BACKEND_PRICE_*`. Both MiMo deployments are priced from **`api.xiaomimimo.com`** whatever endpoint rongo calls, because a token plan lists them at 0 and a reseller at a markup. Unpriceable model → tokens only + a log warning, never a guess.

## Invariants (must hold in every feature)

### Answering
- **Never store or embed model-written text *about* code** — no module, file or symbol summary, eager or lazy. Name candidates per turn, only when a human sees them. Measured and lost: `2026-08-17-module-ranking-and-comments.md`.
- **Never invent.** A chain leading into non-indexed code → say so: call and configuration are visible, internals are not.
- **No hit means no hit** — "nothing found" plus the terms tried, never an answer from whatever is in context.
- **Every claim is citable**: repo, branch, file, line, indexed commit. Sources open in rongo's own viewer at that commit, never a forge link. Cited files are never evicted when capping context.
- **Code is truth, docs are context.** A contradiction is named in the answer, which sides with the code. Docs are demoted in fusion by a decay, never filtered (`DocDecay = 0.7`; 0.5 costs 2 of 3 doc-led questions — `2026-09-05-doc-demotion.md`), doc-only modules stay off the card, and an all-docs turn says so above the answer and reports what the document states, not what the system does. Plans and mock-ups → `BACKEND_INDEX_EXCLUDE`, never a broad doc exclusion.
- **Re-sweeping `DocDecay` reads r@20 AND r@5 AND mean rank** (0.7 moved mean rank 2.56 → 2.40, flat below): membership at the cut cannot see a README going from rank 1 to 8. **Mean rank over the questions EVERY arm ranks**, never each arm's own hits — otherwise an arm admitting one more question at rank 19 reports a degradation that never happened.
- **A diagram cites like prose**: one fence per answer, every node lists its sources, same chips and same viewer. **A node cites code or nothing** — doc markers are stripped from a node before reader numbers are assigned, and the node is still drawn with no sources rather than dropped. The predicate is the file's spelling, not its ranking role, because a dropped chip is unrecoverable where a demotion only costs rank. A doc-only claim is named in the sentence instead. Undrawable spec → code block; an oversized one is drawn, never dropped.

### Threads
- **The thread is a record.** A follow-up adds an answer, never rewrites one. A correction is a new question.
- **One language per thread** — its first question's. Everything a person reads follows it, including replays: pills, re-explains, retries, resumes. The record decides it, not the request.
- **A follow-up knows what it follows**: the pin plus the last answered question and answer, and only the understanding step sees the answer. The answer prompt gets the previous QUESTION only — prior prose sitting beside real sources is how a claim acquires a citation it was never read from. One turn back, never the thread.
- **Recall reaches the model as ONE user message**, never a user/assistant pair: a prose assistant turn makes the model continue the conversation instead of returning JSON.
- **A thread is a funnel: it narrows, never widens.** The pin is a ceiling — a question may narrow inside it, never past it, and "in all repos" under a pin is not honoured. A named repo the pin excludes is reported as outside, with a prompt rule forbidding claims about it; refusing to widen silently is the same quiet drop that rule exists to stop.
- **An empty repo restriction means the whole corpus** — so a pin or a chosen repo that no longer exists must FAIL the turn, never search. This trap has bitten twice.
- **A thread is addressed by its public id, never its row number**, and that id is a different value from a share token: a share is revocable authorisation, an address is not. It is the only thread identifier on the wire. A row-number URL is dead and not redirected.

### Routing
- **A clarification is answered once.** The choice starts a new turn and the card becomes a record — collapsed, reopenable, inert, a second resume refused. The answer is what closes it, so a failed turn leaves it open for a retry.
- **The card offers PROJECTS, never repositories.** `repos.yaml` is a `projects:` list, each block holding its own `repositories:`; a repository standing alone is a project of one named after itself. A project is a product — the one option an Analyst can always tell apart — where backend-vs-ui is a *layer* and never a card. Candidates inside one project compose; naming a MEMBER still narrows to it, because a thread narrows and never widens.
- **Fold by project AFTER `repoCandidates`, never inside it.** `anyDependency` hands a candidate's `Repo` to `repodeps.DependsOn`, which joins `repo_deps` on a repository name — a project name has no rows there, and the eval cannot catch the loss because its corpus is one project per repository.
- **`part`, `description` and `uses` are declared, never inferred**, and reach the answer prompt. With two backends in a project only the `uses` edge says which one the storefront calls, and the members nothing calls are named too — that absence is the disambiguating half. The block is configuration: never cited, never presented as read from code. `uses` stays inside one project; cycles are fine.
- **A project turn is not a comparison of its own repositories.** `Scope.Projects` lists projects `Known` covers ENTIRELY; one suppresses the comparison rule, two or more compare by project name. A partial cover counts for nothing, and any repository left over (`Scope.Loose`) drops the turn back to the repository-grained comparison.
- **A chosen card entry searches the members it STORED**, never a fresh lookup: a project gaining a member between card and click must not widen the reader's choice.
- **Clarify only on real ambiguity.** Candidates that depend on each other per `repo_deps` are composition — answer all of them. **Any** named indexed repo → never a card, decided above the margin and before anything is paid for. Two or more named is a comparison: one search per repo, and the answer covers each. Exactly one means the reader pointed at a product, so what is still ambiguous inside it is modules or layers → compose.
- **Never answer across repos the question did not ask for.** No repo named and candidates spanning two or more → a repository card, deterministic, above both the margin and the judge: a leader says which module scored best, never which product was meant. Only the reader's own words or a manifest edge override it.
- **More repos than a card fits → do not ask, make them narrow.** Past `maxRepoCandidates` (4) a card would show four and never mention the rest, and one manifest edge is not permission to spread `searchK` over twenty repos. No model call and no "all repositories" way out — answering across all of them is what the rung refuses. The narrowing panel takes at most `maxNarrowRepos` (3), each costing its own full-depth search, and the cap is enforced server-side.
- **The card asks what the ROLE can answer.** Last rung, Analyst only, judged after naming because it judges the card and not the code: options tellable apart only by implementation, layer or package get no card. It **does not gate the repository card** — those options are products, the one thing an Analyst can always tell apart, and gating it would restore the cross-repo answer the rung below exists to stop.
- **Choosing a repo re-searches it** — the one resume that searches again, because a repo card grouped a single fused list and the runner-up's few chunks are not an answer.
- **A named repo the index lacks is said out loud.** Search drops the name so a mishearing cannot wipe the result, and the turn carries a notice plus a prompt rule forbidding claims about it. Stored with the turn, so resume and re-explain answer under the same rules.
- **Cross a repo boundary only with two reasons**: the gathered code really references the symbol, and the target repo is indexed. Same hop budget.

### Repos and index
- **A structure edit is not a re-index.** Changing `project`/`part`/`description`/`uses` leaves `last_sha`, `branch` and the checkout alone; the reset trigger stays `clone_url`.
- **`repos.Load` refuses**: a project block with no name, with no repositories, or repeated (one block per product, whole); a project sharing a name with any repository other than its ONE member (a project of one named after itself is the normal case; the same name over two members would widen the search when the reader means the repository); a `uses` entry unknown, cross-project or self; one `clone_url` twice in a project.
- **Repo list lives in `repos.yaml`, credentials never do** — that file reaches a repo or a ticket eventually. Tokens come from `BACKEND_*` env vars, injected at fetch time, never logged. The Repos page is read-only status, with no CRUD form.
- **Never default a branch to `master`** — an omitted branch resolves the remote's default. The corpus is mixed: peeq/loom/rongo are `master`, `ncruces/go-sqlite3` and `asg017/sqlite-vec` are `main`.
- **One branch per entry**, named (`shop-backend@release-2024.3`), so no two cards can differ only by branch.
- **A configured branch vanishing upstream is a loud error on the Repos page**, never a silent stop — otherwise the index freezes while looking healthy.
- **A repo dropping out of `repos.yaml` is purged** — row, files, chunks, both mirrors, checkout. `enabled: false` parks one instead. Purging by hand needs `purgeContent`'s per-file order: the FK cascade misses `chunks_vec`/`chunks_fts`. Floor under it: `repos.Load` REFUSES a list naming no repository, so a truncated file cannot wipe the corpus. Neither can a half-migrated one: any leftover top-level `repositories:` is refused by name even beside a valid `projects:` block, and the decode is `KnownFields(true)` so an unknown key is an error, never a silently ignored entry.
- **The checkout's `origin` is the identity, the directory name is only a label.** A `clone_url` that no longer matches the checkout resets the repo and re-clones, or one repo's code answers under another's name.

### Sharing and routes
- **A share link exposes ONE thread, frozen where it was shared.** Turns asked afterwards stay invisible until the owner raises the ceiling, so a link never grows behind their back. The ceiling is the newest FINISHED turn, never the newest row — otherwise a turn still streaming, or a row orphaned by a crash, could lock a thread out of sharing.
- **Revoking is a flag, not a delete**, so re-sharing returns the SAME link instead of stranding the one already sent. Live links are listed on the Shared page; revoked ones are not — that page is an audit, not a history.
- **Unknown, revoked and deleted are ONE 404.** Never an existence oracle. Public routes set `X-Robots-Tag: noindex`.
- **A shared page carries no usage, cost, model name or follow-up**, and its citations serve only what a turn under the ceiling cites — never the corpus-wide source endpoint. It is the only unauthenticated output path in the product.
- **A path the app has no page for is a REAL 404**, never 200 and a shell that renders the unasked question. A thread address is checked there by SHAPE and never for existence — that handler has no session and no database, and answering would say which addresses are real.

## UI
- Expandable → chevron, rotates 90° on open, **towards what it opened**: down for a panel under its control, up for the trace, whose toggle is the last row under its own steps. No triangle, plus/minus or glyph swap.
- Activity trace is a timeline, **one per turn**: it grows live with the time each step took, and **while the turn runs it is expanded with no toggle on screen** - progress is watched, never shut. Once the turn closes the steps roll up behind the closing row, which carries the node, the total and the chevron; re-opening sticks, because the roll-up fires once, on the running -> closed transition. **The trace is part of the record**: stored per message as instants (`messages.steps`, the turn's own start and close beside the steps), served with the thread, and drawn rolled up on a turn read back — a card that comes back without its ochre "your move" row is the record contradicting itself. Never on a shared link; it goes with the usage. A turn older than the column, or one that announced nothing, draws none.
- Ochre means "your move" — a waiting clarification, not the Done check. Once decided it loses the colour.
- **A streaming answer follows the view until the reader takes it**, and only the next turn re-arms; being back at the foot does not. Watch intent events (wheel, touchmove, pointerdown, a source opened), never the scroll event alone — a scroll lands after the next token has already pulled the view back, and markdown resolving shortens the column, so the browser's clamp is not the reader.
- Warm Editorial dark; same `@theme` and fonts as ../loom and ../peeq. Reference: `docs/plans/rongo-ui-mock.html`.
- Roles read "Analyst" and "Developer"; the wire values stay `ba`/`dev`.
- **The sources pane answers to the audience of the turn it shows**: open for a Developer, shut for an Analyst, who was given an explanation with no paths in it on purpose. Closed by its own `×`, reopened by a chip under the answer, and the reader's click then wins for the rest of the thread; another thread asks again. The chip is on the source turn ONLY - the pane shows the newest citing turn, so a chip elsewhere opens somebody else's sources. Only from `xl`; narrower, the per-answer disclosure is the sources and stays.
- **Every page is 900px and centred** - Ask, Repositories, Shared, a share link. One cap and one centring is what keeps the two pages' first lines on a single rule; change it in one place and it stops holding.
- **Everything a person reads follows the answer language**; identifiers, paths, code and the role names stay untranslated, because a body naming "un Analyste" beside a button reading "Analyst" points at nothing. Chrome and model-internal calls stay English. Prompts name the language first AND last.
- **German is Swiss**: `ß` → `ss` ALWAYS, and umlauts stay umlauts — name `ä ö ü` and forbid `ae/oe/ue` explicitly, because the `ß` rule alone was over-applied into "Sequenzdiagramm fuer Geschaeftsprozesse".
