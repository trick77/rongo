# rongo

Answers questions about indexed code for two audiences: BA (business language) and DEV (technical).
Product name is always `rongo`.

## Conventions
- Docs, specs, code comments: English. UI copy and generated answers: German, Swiss orthography — never `ß`.
- Feature branch per phase (`feat/phase-N-...`). Never commit to `master`.
- TDD: failing test first, then the smallest implementation.
- `.yaml` never `.yml`. `Containerfile` never `Dockerfile`.
- No test hits a real LLM, embeddings endpoint or git remote — `httptest` fakes, fixture repo built
  locally with `git`.
- Never commit built SPA assets; only the tracked placeholder `backend/web/dist/index.html`.

## Locked choices (do not change without agreement)
- Pure-Go SQLite: `ncruces/go-sqlite3` + `asg017/sqlite-vec-go-bindings/ncruces` (wasm) + FTS5.
  `CGO_ENABLED=0` everywhere.
- One SQLite file is the whole datastore. No Postgres, no Redis, no vector service.
- stdlib `net/http`. No web framework, no ORM, no router library.
- **No tree-sitter** — needs cgo, plus a grammar and its node names per language. `ctags` gives a
  uniform record for ~150 languages; where it yields nothing, the line window is the normal path.
- Runtime image stays non-distroless: rongo shells out to `git`, `rg`, `ctags`.
- Config only via `RONGO_*` env vars.

## Models
- Two MiMo deployments, hardcoded in `internal/llm/client.go`, never env vars.
- **Pro** only where a human reads: the answer, feature-card summaries.
- **non-Pro + `ShortGate`** for everything else: understand, route, relevance while gathering, thread
  title, follow-up sufficiency check. The bar is "output is an id or a label", not "doesn't think".
- Both deployments reason. `WithoutThinking` and `ShortGate` are separate switches; don't couple them.
- Cap every call with `WithMaxTokens` unless a truncated reply would be worse than a long one.

## Invariants (must hold in every feature)
- **Never invent.** Chain leads into non-indexed code → say so in the answer: call and configuration
  are visible, the internals are not.
- **No hit means no hit.** "Nothing found" plus the terms tried — never an answer built from whatever
  happens to be in context.
- **Every claim is citable**: repo, file, line. Cited files are never evicted when capping context.
- **The thread is a record.** A follow-up adds an answer, never rewrites one. A corrected
  clarification choice starts a new turn.
- **Clarify only on real ambiguity.** Candidates that depend on each other per `repo_deps` are
  composition, not alternatives — answer all of them instead of asking.
- **Cross a repo boundary only with two reasons**: the gathered code really references the symbol,
  and the target repo is indexed. Same hop budget, no discount for crossing.
- Repository credentials: encrypted at rest, never logged, never returned by the API.

## UI
- Expandable → chevron, rotates 90° on open. No triangle, no plus/minus, no glyph swap.
- Activity trace is a timeline, **one per turn**. A clarification ends the turn: ochre waiting node,
  not the Done check.
- Ochre means "your move". Once something is decided, it loses the colour.
- No boustrophedon in the UI — deliberately dropped, legibility beats motif.
