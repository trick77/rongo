# rongo

Numbers: `docs/measurements/`; bullet cites one → read it before overturning.

## Conventions
- English: docs, comments, UI copy, prompts, answers (maintainer talks German).
- Feature branch per phase; never commit to master.
- TDD: failing test first, then smallest implementation.
- No test hits a real LLM, embeddings endpoint or git remote: `httptest` fakes, local fixture repo.
- Gate = ship (`make check`, pre-push hook): coverage 75% both sides + changed lines. `ship init` files: fix in ship, never here.
- Fresh clone: `ship hooks` before the first push (check, conform and init install it too); without the pre-push hook a push skips the gate.
- Commit subjects + PR titles: Go style `area: what`, gated by ship; release notes list them by area.
- New config → `BACKEND_*` env var, nowhere else. Malformed refuses to start; empty = default.
- Database: read `docs/schema.md` first; regenerate per its header. `TestSchemaDoc` fails on migrations, model.go struct docs (first paragraph), `Table:` lines, `Table: -` fields; skips table-bound fields, never checks a real column.

## Locked (change needs agreement)
- Pure-Go SQLite: ncruces + sqlite-vec wasm bindings + FTS5, as peeq/loom. Pair bumps by hand, together (`ship.yaml`).
- One SQLite file = whole datastore (no Postgres, Redis, vector service); stdlib `net/http` (no framework, ORM, router).
- `ctags`, never tree-sitter (`internal/symbols/ctags.go`: why); else the line window. Must be universal-ctags: macOS BSD ctags gives an empty symbol index, no error.
- No `rg`: index holds every file worth searching; its substring lane is the text search.
- Wire layer = llmwire: gaps, new models land there first. Prices from its `profiles.yaml`, never from tokens. Models = profile ids, never hosts; hosts, keys = `LLMWIRE_*`, never read by rongo.
- `embed.Model` is a build constant, never config: other model = new database.
- Auth, sessions, rate limit, SPA, middleware, env, server are kit's. One exception, `internal/store/migrate.go`: `0003_*`, `0010_*` share a version, kit's runner refuses that, renaming history rewrites every database.

## Models
- Model change judged by `TestEvalMeasureAnswers`, run twice. Retrieval numbers show what an answer was built from, not if it was right.
- No model id, reasoning level, limit, host or rate in rongo code: llmwire profile. Lanes by `llm.Lane`, never model string.
- Example `claude-opus-5-5`, both lanes: eval-backed (`2026-10-26-opus-both-lanes.md`); prior mimo-v2.6-flash pair was speed alone. Eval judge stays mimo-v2.6-flash: it graded every number on record.
- Answer lane where a human reads; gate lane + `ShortGate` elsewhere. Bar: output is an id or label. `WithoutThinking`, `ShortGate`, `WithGateTemperature` never coupled.
- Reasoning is intent. Gates minimal (`BACKEND_LLM_GATE_REASONING` stays retired). 15m default timeout = hard cap, never raised. Balanced anywhere new = model change, needs eval.
- Routing judge on gate lane: two measurements went opposite ways (`2026-08-19-candidates.md`, `2026-09-06-routing-rerun.md`); no move back without corpus + number.
- Pin `WithGateTemperature` on every id/label/decision call, else the judge re-rolls. Pinning narrows the re-roll, never removes it: no conclusion from a one-question gap. Run twice.
- Never rank routing by accuracy: most questions want no card, so never-asking scores well, pulls thresholds to silence (`2026-09-06-routing-cost-metric.md`).
- Caps size the ANSWER; llmwire adds reasoning, never hand-size for it.

## Answering
- Never store or embed model text *about* code (module, file, symbol summary): goes stale, pulls a vector toward a claim no code honours. Reopen only with a `TestEvalMeasureAnswers` number, then embed-only, never shown.
- Code is truth, docs context: decay-demoted, never broad-excluded (`2026-09-05-doc-demotion.md`). Plans, mock-ups → `BACKEND_INDEX_EXCLUDE`.
- Prompt changes judged on a number, never a hunch: negative diagram trigger drew 1 diagram in 19 BA answers, positive 13 (`2026-09-17-ba-diagrams.md`). Diagram failing to draw → corpus as `.txt` FIRST.
- Compare arms inside ONE database: deterministic; two FRESH indexes differ by embedding drift at the 4th decimal (`2026-09-16-stable-order.md`). Say so when a number spans a rebuild.

## Threads, routing
- Empty repo restriction = whole corpus: pinned or chosen repo gone → FAIL the turn, never search. Bit 2x.
- Never gather outside the turn's projects (+ `uses` libraries): one ceiling, every path. Live turn: one project named, 11 repos gathered.
- Card offers PROJECTS, never repos: the one option an Analyst tells apart. Interdependent candidates = composition: answer all, no card.
- Cross a repo boundary only with two reasons: gathered code really references the symbol, target repo indexed.
- Measured OFF, stay off: route suffix matching, whole-file reading, gate `json_object` (`2026-09-13-json-object.md`), second FTS column of paths + split identifiers (`2026-09-16-code-rung.md`), excluding test files: labelled instead (`2026-09-17-test-label.md`). Knobs stay; tables say why.

## Repos, index
- Repos page: read-only, no CRUD; list = `repos.yaml`.
- One named branch per entry: no two cards differ only by branch.
- New extraction rule needs a forced re-index.
- Credentials never leave the machine: regex, not prompt. New shape → `redact_test.go`, invented value.
- Log durations as `.String()`: only a string reads the same under every handler.

## UI
- Warm Editorial dark; `@theme` + fonts as ../loom, ../peeq.
- Expandable: chevron, turns 90° on open toward what it opened; no triangle, plus/minus, glyph swap.
- Ochre = "your move": waiting clarification, not the Done check; gone once decided.
- German is Swiss by string function (`swiss.go`), never a prompt note: measured, moved nothing.
