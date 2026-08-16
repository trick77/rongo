# rongo

## Conventions
- Docs, specs, code comments: English. UI copy and generated answers: German, Swiss orthography —
  never `ß`.
- Feature branch per phase (`feat/phase-N-...`). Never commit to `master`.
- TDD: failing test first, then the smallest implementation.
- `.yaml` never `.yml`. `Containerfile` never `Dockerfile`.
- No test hits a real LLM, embeddings endpoint or git remote — `httptest` fakes, fixture repo built
  locally with `git`.

## Locked choices (do not change without agreement)
- Pure-Go SQLite: `ncruces/go-sqlite3` + `asg017/sqlite-vec-go-bindings/ncruces` (wasm) + FTS5.
  `CGO_ENABLED=0` everywhere.
- **Those two modules are one unit.** The vec extension is a wasm build compiled against ncruces'
  SQLite, so a version skew breaks vector search rather than failing to compile. Bump both together
  or neither — never one alone, never a lone Dependabot PR. Keep the same pair as peeq and loom
  (`v0.23.3` / `v0.1.7-alpha.2`); divergence means a fix found in one repo does not transfer.
  `v0.1.7-alpha.2` is an alpha: read its changelog before any bump.
- A test must drive `vec0` end to end — create, insert, KNN query, assert the neighbour — so an
  incompatible bump fails CI instead of surfacing as empty result sets in production.
- One SQLite file is the whole datastore. No Postgres, no Redis, no vector service.
- stdlib `net/http`. No web framework, no ORM, no router library.
- **No tree-sitter** — needs cgo, plus a grammar and its node names per language. `ctags` gives a
  uniform record for ~150 languages; where it yields nothing, the line window is the normal path.
- Runtime image stays non-distroless: rongo shells out to `git`, `rg`, `ctags`.
- **`ctags` must be universal-ctags.** macOS ships Apple's BSD ctags at `/usr/bin/ctags`, which
  rejects long options outright — the dev env has no container to hide behind. Verify the binary at
  startup and fail loudly; a wrong ctags yields an empty symbol index, not an error.
- New config goes in a `RONGO_*` env var, nowhere else.

## Models
- Two MiMo deployments, hardcoded in `internal/llm/client.go`, never env vars.
- **Pro** only where a human reads: the answer, feature-card summaries.
- **non-Pro + `ShortGate`** for everything else: understand, route, relevance while gathering, thread
  title, follow-up sufficiency check. The bar is "output is an id or a label", not "doesn't think".
- Both deployments reason. Never justify the non-Pro lane as "the model that can't think".
  `WithoutThinking` and `ShortGate` are separate switches; don't couple them.
- Cap every call with `WithMaxTokens` unless a truncated reply would be worse than a long one.
- A BA answer is the core mechanism in three to five paragraphs — answer the question and stop. Edge
  cases belong in a follow-up, which is cheap because the context is already gathered. DEV gets more
  room for inline code.
- Embeddings are cached by chunk content hash. Never re-embed unchanged content — dev re-indexes the
  whole corpus constantly, and the cache is what keeps that loop bearable.

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
- **Repository list lives in `repos.yaml`, credentials never do.** That file ends up in a repo or a
  ticket sooner or later; tokens come only from `RONGO_*` env vars, one per forge host, injected into
  the remote URL at fetch time and never logged. Don't build a CRUD form for repos — the Repos page
  is read-only status.
- **A repo dropping out of `repos.yaml` is deactivated, not deleted.** It leaves search; its index
  stays until an explicit admin purge. A typo must not destroy hours of indexing.
- **A share link exposes one answer, never a thread**, is individually revocable, and every live link
  is listed in the admin view. It is the only unauthenticated output path in the product.

## UI
- Expandable → chevron, rotates 90° on open. No triangle, no plus/minus, no glyph swap.
- Activity trace is a timeline, **one per turn**. A clarification ends the turn: ochre waiting node,
  not the Done check.
- Ochre means "your move". Once something is decided, it loses the colour.
