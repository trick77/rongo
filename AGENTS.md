# rongo

## Conventions
- Docs, specs, code comments, UI copy, prompts and generated answers: English throughout.
- Feature branch per phase (`feat/phase-N-...`). Never commit to `master`.
- TDD: failing test first, then the smallest implementation.
- `.yaml` never `.yml`. `Containerfile` never `Dockerfile`.
- No test hits a real LLM, embeddings endpoint or git remote — `httptest` fakes, fixture repo built locally with `git`.
- Line coverage floor 75% on both sides (`hack/coverage-floors`), plus 75% on the lines a PR changes. `make coverage` locally; CI gates on both. Scripts are byte-identical with ../peeq and ../loom — fix them there too, never fork.

## Locked choices (do not change without agreement)
- Pure-Go SQLite: `ncruces/go-sqlite3` + `asg017/sqlite-vec-go-bindings/ncruces` (wasm) + FTS5. `CGO_ENABLED=0` everywhere.
- **Those two modules are one unit.** The vec extension is a wasm build compiled against ncruces' SQLite, so version skew breaks vector search, not the build. Bump both or neither, never a lone Dependabot PR. Same pair as peeq/loom (`v0.23.3`/`v0.1.7-alpha.2`) so fixes transfer; read the alpha's changelog first.
- A test drives `vec0` end to end (create, insert, KNN, assert the neighbour) so an incompatible bump fails CI, not production.
- One SQLite file is the whole datastore. No Postgres, no Redis, no vector service.
- stdlib `net/http`. No web framework, no ORM, no router library.
- **No tree-sitter** — needs cgo, a grammar, and per-language node names. `ctags` gives a uniform record for ~150 languages; where it yields nothing, the line window is the normal path.
- Runtime image stays non-distroless: rongo shells out to `git`, `rg`, `ctags`.
- **`ctags` must be universal-ctags.** macOS ships BSD ctags at `/usr/bin/ctags`, rejecting long options; dev runs without a container. Verify at startup and fail loudly — a wrong ctags yields an empty symbol index, not an error.
- New config goes in a `BACKEND_*` env var, nowhere else.

## Models
- Two MiMo deployments, hardcoded in `internal/llm/client.go`, never env vars.
- **Pro** where a human reads: the answer.
- **Pro also for the routing judge** — the one exception, measured: Pro 48/61+50/61 vs non-Pro 42/61+43/61, residual 1–2 (`docs/measurements/2026-08-19-candidates.md`). One word, but it decides answer-vs-question. Don't "restore" it to non-Pro.
- **non-Pro + `ShortGate`** for the rest: understand, candidate naming, relevance while gathering, thread title, follow-up sufficiency check. Bar is "output is an id or a label", not "doesn't think".
- Both deployments reason. Never justify the non-Pro lane as "the model that can't think". `WithoutThinking`, `ShortGate` and `WithTemperature` are separate switches; don't couple them.
- **Pin `WithTemperature(gateTemperature)` on every call returning an id, label or decision.** Unpinned, the judge re-rolled 3 of 61 questions per run — wider than the deployment gap it was compared against. Answer call stays unpinned; a person reads it.
- Cap every call with `WithMaxTokens` unless a truncated reply would be worse than a long one.
- A BA answer is the core mechanism in three to five paragraphs — answer and stop. Edge cases go to a follow-up, cheap because the context is already gathered. DEV gets more room for inline code.
- Embeddings are cached by chunk content hash; never re-embed unchanged content.

## Invariants (must hold in every feature)
- **Never store or embed model-written text *about* code** — no module/file/symbol summary, eager or lazy: scales with corpus not usage, stale when written, and at useful resolution it is the code rewritten in prose. Name candidates per turn, only when a human sees them. Measured and lost: `docs/measurements/2026-08-17-module-ranking-and-comments.md`.
- **Code is truth, docs are context.** README/docs stay indexed for intent; the code decides. Contradiction → answer names it, sides with the code. Plans and mock-ups → `BACKEND_INDEX_EXCLUDE`, never a broad doc exclusion.
- **Never invent.** Chain leads into non-indexed code → say so: call and configuration are visible, the internals are not.
- **No hit means no hit.** "Nothing found" plus the terms tried, never an answer built from whatever is in context.
- **Every claim is citable**: repo, branch, file, line — branch also in the forge URL, else the link may 404 off the default branch. Cited files are never evicted when capping context.
- **The thread is a record.** A follow-up adds an answer, never rewrites one. A corrected clarification starts a new turn.
- **Clarify only on real ambiguity.** Candidates depending on each other per `repo_deps` are composition, not alternatives — answer all of them.
- **Cross a repo boundary only with two reasons**: the gathered code really references the symbol, and the target repo is indexed. Same hop budget.
- **Repo list lives in `repos.yaml`, credentials never do** — that file reaches a repo or ticket eventually. Tokens come from `BACKEND_*` env vars, one per forge host, injected at fetch time, never logged. No CRUD form; the Repos page is read-only status.
- **Never default a branch to `master`** — omitted `branch:` means resolve the remote default via `git ls-remote --symref origin HEAD`. The corpus is mixed: peeq/loom/rongo are `master`, `ncruces/go-sqlite3` and `asg017/sqlite-vec` are `main`.
- **One branch per entry.** A second branch is a second entry with its own name (`shop-backend@release-2024.3`), so it cannot produce cards differing only by branch.
- **A configured branch vanishing upstream is a loud error on the Repos page**, never a silent stop — otherwise the index freezes while looking healthy.
- **A repo dropping out of `repos.yaml` is deactivated, not deleted.** Its index survives until an explicit purge; a typo must not destroy hours of indexing.
- **A share link exposes one answer, never a thread**, is individually revocable, and every live link is listed in the admin view. It is the only unauthenticated output path.

## UI
- Expandable → chevron, rotates 90° on open. No triangle, no plus/minus, no glyph swap.
- Activity trace is a timeline, **one per turn**, **never collapsible**: it grows live as steps arrive, with the time each took. Progress is watched, not opened. A clarification ends the turn: ochre waiting node, not the Done check.
- Ochre means "your move". Once decided, it loses the colour.
- Look and tokens: Warm Editorial dark, the same `@theme` and fonts as ../loom and ../peeq (`ui/src/index.css`). Reference: `docs/plans/rongo-ui-mock.html`.
- Roles read "Analyst" and "Developer" in the UI; the wire values stay `ba`/`dev`.
- **Everything a person reads follows the answer language** (`ask.Language`): answer, clarification card titles and summaries, thread title, nothing-found text. Prompts name the language first AND last; identifiers, paths and code stay untranslated. Model-internal calls (understand, judge, relevance) stay English.
