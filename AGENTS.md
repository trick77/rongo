# rongo

Rules, not description. Code is truth — implementation is discoverable, so it is not written here. Numbers live in `docs/measurements/`; bullet citing one → read it before overturning.

## Conventions
- English everywhere: docs, comments, UI copy, prompts, answers.
- Feature branch per phase. Never commit to `master`.
- TDD: failing test first, then smallest implementation.
- No test hits a real LLM, embeddings endpoint or git remote. `httptest` fakes, fixture repo built locally.
- Gate is ship (`make check`, pre-push hook): coverage floor 75% both sides, 75% on changed lines. Files `ship init` owns are never edited here — fix in ship, never fork.
- Fresh clone: `ship hooks` before the first push (check, conform and init install it too); without the pre-push hook a push skips the gate.
- Commit subjects + PR titles: Go style `area: what`, gated by ship; release notes list them by area.
- New config → `BACKEND_*` env var, nowhere else. Malformed value refuses to start; empty means the default.
- Database: read `docs/schema.md` first. `TestSchemaDoc` golden fails on doc drift only: schema-changing migrations, model.go structs (first doc paragraph), `Table:` lines, fields of `Table: -` types. Fields of a table-bound type are not in the doc, so unchecked; no field is checked against a real column. Regenerate in `backend/` (doc header names the command).

## Locked choices (no change without agreement)
- Pure-Go SQLite: `ncruces/go-sqlite3` + `asg017/sqlite-vec-go-bindings/ncruces` (wasm) + FTS5. One unit: bump both or neither, by hand (`ship.yaml` says why). Same pair as peeq/loom.
- One SQLite file is the whole datastore. No Postgres, Redis, vector service.
- stdlib `net/http`. No framework, ORM, router.
- **No tree-sitter** (cgo). `ctags` covers ~150 languages; else the line window.
- No `rg`: the index holds every file worth searching, and its substring lane is the text search.
- **`ctags` must be universal-ctags** — macOS's BSD ctags yields an empty symbol index rather than an error.
- Wire layer is llmwire. A gap is a llmwire PR, a new model goes there first. Prices from its `profiles.yaml`, never derived from tokens. Models are profile ids, never hosts; hosts and keys are `LLMWIRE_*`, never read by rongo.
- `embed.Model` is a constant of the build. A different one is a new database.
- Auth, sessions, rate limit, SPA, middleware, env, server are kit's. One exception, `internal/store/migrate.go`: two migrations share a version number (`0003_*`, `0010_*`), kit's runner refuses that, renaming history would rewrite every database.

## Models
- **Model change is judged by `TestEvalMeasureAnswers`**, run twice. Retrieval numbers say what the answer was written from, never whether it was right.
- **No model id in code; model swap = config + key.** Every model fact (levels, limits, hosts, prices) is llmwire's profile. Lanes told apart by `llm.Lane`, never by model string. A reasoning level, host or rate in rongo's code is a bug.
- **Example model `claude-opus-5-5`, both lanes, judged by eval (`2026-10-26-opus-both-lanes.md`).** The mimo-v2.6-flash pair before it was chosen on speed alone, no eval number behind it. **Eval judge stays on mimo-v2.6-flash**: every number on record was graded by it.
- **Answer lane where a human reads; gate lane + `ShortGate` everywhere else.** Bar is "output is an id or a label", not "doesn't think". `WithoutThinking`, `ShortGate`, `WithGateTemperature` are separate switches. Don't couple them.
- **Reasoning is intent, never a level in code.** Gates stay minimal (`BACKEND_LLM_GATE_REASONING` stays retired). 15m default timeout is a hard cap, never raised. Balanced anywhere new = a model change, needs the eval.
- **Routing judge on the gate lane.** Two measurements went opposite ways (`2026-08-19-candidates.md`, `2026-09-06-routing-rerun.md`). No move back without a corpus and a number.
- **Pin `WithGateTemperature` on every call returning an id, label or decision**; unpinned, the judge re-rolls. Pinning narrows the re-roll, never removes it: never conclude from a one-question gap. Run twice.
- **Never rank routing by accuracy.** Most questions want no card, so never-asking scores well and drifts every threshold toward silence (`2026-09-06-routing-cost-metric.md`).
- Size every cap for the ANSWER; llmwire adds the reasoning allowance. Never hand-size a cap to cover thinking.

## Invariants

### Answering
- **Never store or embed model text *about* code** — no module, file or symbol summary. It goes stale like documentation and pulls a vector toward a claim no code honours. Reopen only with a `TestEvalMeasureAnswers` number, then embed-only, never shown.
- **Code is truth, docs are context.** Docs demoted by a decay, never a broad doc exclusion (`2026-09-05-doc-demotion.md`). Plans and mock-ups → `BACKEND_INDEX_EXCLUDE`.
- **Prompt changes read against a number, never a hunch.** Negative diagram trigger drew 1 diagram in 19 BA answers, positive 13 (`2026-09-17-ba-diagrams.md`). A diagram that fails to draw goes into the corpus as `.txt` FIRST.
- **Compare arms inside ONE database.** One database is deterministic; two FRESH indexes differ by the embedding endpoint's fourth-decimal drift (`2026-09-16-stable-order.md`). Say so when a number spans a rebuild.

### Threads
- **Empty repo restriction means the whole corpus** — a pin or chosen repo that no longer exists must FAIL the turn, never search. Has bitten twice.

### Routing
- **Never gather outside the turn's projects** (plus libraries in `uses`); every gather path admits through that one ceiling. A live turn named one project and gathered from 11 repositories.
- **Card offers PROJECTS, never repositories** — the one option an Analyst can tell apart. Candidates depending on each other are composition: answer all, no card.
- **Cross a repo boundary only with two reasons**: gathered code really references the symbol, target repo is indexed.
- **Measured OFF, stay off**: route suffix matching, whole-file reading, `response_format: json_object` on gate calls (`2026-09-13-json-object.md`), a second FTS column of paths and split identifiers (`2026-09-16-code-rung.md`), excluding test files: labelled instead (`2026-09-17-test-label.md`). Knobs stay in code with those tables as the reason.

### Repos and index
- Repos page is read-only status, no CRUD form; the list lives in `repos.yaml`.
- **One branch per entry**, named, so no two cards differ only by branch.
- **A new extraction rule needs a forced re-index.**
- **A credential value never leaves the machine, by regex, not prompt.** New shape → `redact_test.go` with an invented value.
- **Log durations as `.String()`** — a `time.Duration` attr reads the same under every handler only as a string.

## UI
- Warm Editorial dark; same `@theme` and fonts as ../loom and ../peeq.
- Expandable → chevron, rotates 90° on open, **toward what it opened**. No triangle, plus/minus, glyph swap.
- Ochre means "your move" — a waiting clarification, not the Done check. Once decided it loses the colour.
- **German is Swiss by string function, never prompt** (`swiss.go`). A prompt note asking for the same spelling was measured and moved nothing — do not add one back.
