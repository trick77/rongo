# Continuation prompt — rongo phase 2, tasks 5 to 10

Paste everything below the line into a fresh session.

---

You are continuing work on **rongo**, a Go service that indexes code repositories and answers questions about them for two audiences: business analysts (BA) and developers (DEV). Phase 1 (the skeleton) is merged to `master`. Phase 2 (indexing) is in progress and you are picking it up at task 5 of 10.

## Where things are

- **Worktree:** `/Users/jan/localgit/rongo/.claude/worktrees/phase-2-indexing`, branch `feat/phase-2-indexing`. Work only there. Go module root is `backend/`.
- **The plan you are executing:** `docs/plans/2026-08-17-rongo-phase-2.md` — read the task you are on, in full, before writing anything.
- **The design spec, which is the binding authority when the plan is ambiguous:** `docs/plans/rongo-spec.html`.
- **Repo conventions, binding:** `AGENTS.md` at the worktree root. Read it first.
- **Execution ledger:** `.superpowers/sdd/2026-08-17-rongo-phase-2/progress.md`. Append to it after every task; it is the recovery map if your session dies.

## What already exists — do not rebuild it

Tasks 1 to 4 are committed and green:

- `internal/repos` — `Load(path) ([]Spec, error)`, `Spec{Name, CloneURL, Branch, TokenEnv string, Enabled bool}`. `Branch` is **empty** when the YAML omitted it; resolving it is the git layer's job. Credentials are rejected in `clone_url`.
- `internal/gitrepo` — `New(gitBin, root) *Client` with `EnsureCloned`, `DefaultBranch`, `Fetch`, `HeadSHA`, `ChangedPaths`, `ListPaths`, `ReadFile`, and `ErrBranchGone`.
- `internal/indexer` — `NewStateStore(db)` with `SyncSpecs`, `Active`, `All`, `MarkIndexed`, `MarkError`, `SetBranch`; `RepoState`, `Counts`; `NewPoller(PollerDeps)` with `Run` and `PollOnce`; `IndexFunc` = `func(ctx, RepoState, sha string, paths []string) (Counts, error)`; `NewSelector(SelectOptions)` with `Select(path, body) (Decision, string)` and `LanguageOf(path)`.
- `internal/sched` — `Jittered`, `Sleep`.
- Migration `0002_index.sql` already creates every table the remaining tasks need: `repo_state`, `files` (with `skip_reason`), `symbols`, `chunks`, `chunks_vec`, `chunks_fts`, `embed_cache`, `repo_deps`. **Do not add a migration for these.**
- `config.Config` carries `ReposFile` and `IndexMaxFileBytes`; every setting is `BACKEND_*` and goes through `envOr`/`envIntOr`, which trim.
- `main.go` wires the poller with a **stub** `Index` func returning zero counts. Task 8 replaces that stub with the real pipeline — that is the wiring you must change, not the poller.

## Conventions that are not negotiable

- `CGO_ENABLED=0` everywhere. Never add a cgo dependency.
- `ncruces/go-sqlite3` v0.23.3 and `asg017/sqlite-vec-go-bindings` v0.1.7-alpha.2 are **one unit**: never bump one alone.
- One SQLite file is the whole datastore. stdlib `net/http` only. No ORM, no framework.
- **No tree-sitter.** `ctags` gives a uniform record across ~150 languages; where it yields nothing, the line window is the normal path, not a failure.
- Structured `slog` only, error attribute key `err` (never `error`). Never log a token, a full URL, or a query string.
- Docs and code comments in **English**. UI copy and generated answers in **German, Swiss orthography — never `ß`**.
- No test may hit a real LLM, embeddings endpoint, or git remote. Use `httptest` and a fixture repository built locally with `git init`.
- TDD: failing test first, watch it fail, then implement.
- Commit as `trick77 <trick77@users.noreply.github.com>`. Never `--author`, never a `Co-Authored-By` trailer, never a "Generated with" line. Never commit to `master`.

## Operating conditions you should know

This machine has been suspending processes without warning. Nine background agents were killed mid-task overnight. Consequences:

- **Commit as soon as the tests pass**, before writing any report. Twice the work was finished and lost only the commit.
- If you dispatch subagents and they die, stop dispatching and do the work directly in short steps.
- After any interruption, run `git status` and `git log` before redoing anything — the work is often already on disk.

## What is left

### Task 5 — symbol extraction via universal-ctags
`internal/symbols`: `NewExtractor(ctagsBin)` with `Extract(ctx, path, body) ([]Symbol, error)`, `Symbol{Name, Kind, Scope string, Line int}`. Invoke ctags with JSON output. `exttools.Resolve` already guarantees at startup that the binary is universal-ctags, not the BSD one macOS ships.

Two things to get right: a language ctags does not know must return an **empty slice and no error** — that is the normal path, and the chunker falls back to line windows. But unparseable output must return an **error**: zero symbols and broken output look identical downstream and mean opposite things. A ctags failure on one file must not fail the repository.

### Task 6 — chunking and enrichment
`internal/indexer/chunk.go`. Read `peeq/backend/internal/rag/chunk.go` first; the token estimator (~4 chars/token, deliberately not tiktoken) and overlap handling port directly. Target ~600 tokens, 800 ceiling, ~75 overlap.

The part that is not a port, and is the whole point: `Text` (what gets embedded) is **enriched** — breadcrumb, enclosing symbol names, doc comment, then the body. `RawText` is the source only. A question in business language and a method body share almost no words, so the vector lane needs the enrichment; the keyword lane must match the literal identifier, so it indexes `RawText`. `ContentHash` is computed over **RawText only**, so reformatting a breadcrumb does not invalidate the embedding cache.

### Task 7 — embedding client and content-hash cache
`internal/embed`. Read `peeq/backend/internal/rag/embed.go` and port the request shape, batching and error-body cap.

Test the ordering explicitly: a batch must return vectors **in input order**. Out-of-order results silently pair every chunk with someone else's embedding and nothing downstream would notice. A returned vector whose length differs from the configured dim is an error, not a stored short vector. The cache is keyed by `(content_hash, model)` — the same hash under a different model must be a MISS, which is what makes task 10's comparison honest.

### Task 8 — the write path and the pipeline
`internal/indexer/write.go` and `index.go`. `ReplaceFile` writes `files`, `chunks`, `chunks_vec` and `chunks_fts` **in one transaction**; a failure partway must leave none of them written, or the vector lane and the keyword lane disagree about what exists and every later result set is quietly wrong. `chunks_vec` and `chunks_fts` rowids must equal `chunks.id` — vec0 cannot take part in triggers or FK cascades, so this bridging is maintained by hand and is exactly the thing that rots silently. Assert it by joining all three.

A file the selector skipped is still recorded in `files` with its `skip_reason` and zero chunks. Then replace the stub `Index` func in `main.go` with the real `IndexRepo`.

### Task 9 — hybrid retrieval
`internal/retrieve`. Read all three of `peeq/backend/internal/rag/{store,query,hybrid}.go` before writing anything, and port rather than reinvent. `vec0` needs the `k = ?` constraint alongside the `MATCH`; the distance bound is a post-filter. Fusion is weighted Reciprocal Rank Fusion, k=60, with the **keyword lane weighted above the semantic one** — unweighted, semantic noise outranks a literal match, and the literal lane is what finds `PromoMailJob` when someone types the identifier.

The repository filter must be a **pre-filter** (`rowid IN (...)`), not applied afterwards: otherwise one repository's chunks consume the whole top-k and the requested repository returns nothing. Write the test that pins this. A query with no matches returns an empty slice and **no error** — "no hit means no hit"; an error would be indistinguishable from a broken database.

### Task 10 — measurement, and the embedding-model decision
Build a fixed question set against the dev corpus and measure recall@5, recall@20 and MRR for `text-embedding-3-small` versus `-large`, then record the decision in `docs/measurements/`. Write the questions from code you have actually read — a question whose expected path you guessed makes the measurement worthless. Cost is already settled and is not a factor: the whole dev corpus embeds for about five cents either way, so this is purely about retrieval quality. If the difference is inside the noise, choose `small` and say so explicitly.

## How to verify, every task

```
cd backend && go test ./... && go vet ./...
gofmt -l backend        # must print nothing
cd backend && CGO_ENABLED=0 go test ./...
```
`git status --short` must be empty afterwards — no built assets, no stray files.

## How to review your own work

Before calling a task done, write down the one thing an attacker or a skeptical reviewer would go after, then actually try it. This found a real token leak in phase 2's git client: `redact()` was tested with a bare URL and passed, but git wraps the remote in single quotes, `url.Parse` then fails, and the token went straight into the log. The value was in naming the attack precisely, not in who executed it.

## When phase 2 is done

Run a whole-branch review, fix what it finds, open a PR against `master`, and run `/code-review medium <PR#>`. The user has authorised merging a phase PR once it passes a medium code review — a red or unresolved review still blocks it. Report what you merged and what you deliberately left undone.
