# rongo

Rules, not description. Code is truth — implementation is discoverable, so it is not written here. Numbers live in `docs/measurements/`; bullet citing one → read it before overturning.

## Conventions
- English everywhere: docs, comments, UI copy, prompts, answers.
- Feature branch per phase. Never commit to `master`.
- TDD: failing test first, then smallest implementation.
- `.yaml` never `.yml`. `Containerfile` never `Dockerfile`.
- No test hits a real LLM, embeddings endpoint or git remote. `httptest` fakes, fixture repo built locally.
- Coverage floor 75%, both sides, plus 75% on changed lines. `scripts/coverage-*` and `scripts/strip-comment-lines.go` byte-identical with ../peeq's `hack/` copies — fix in the family, never fork. Directory renamed here first; peeq follows, then this note loses the `hack/`.
- Tests need `-race` (cgo). Binary stays `CGO_ENABLED=0`.
- New config → `BACKEND_*` env var, nowhere else.

## Locked choices (no change without agreement)
- Pure-Go SQLite: `ncruces/go-sqlite3` + `asg017/sqlite-vec-go-bindings/ncruces` (wasm) + FTS5.
- **Those two are one unit** — vec is wasm compiled against ncruces' SQLite, skew breaks vector search at runtime, not the build. Bump both or neither, never a lone Dependabot PR. Same pair as peeq/loom.
- One SQLite file is the whole datastore. No Postgres, Redis, vector service.
- stdlib `net/http`. No framework, ORM, router.
- **No tree-sitter** (cgo). `ctags` covers ~150 languages; else the line window.
- Runtime image non-distroless: rongo shells out to `git`, `rg`, `ctags`.
- **`ctags` must be universal-ctags** — macOS ships BSD ctags, wrong one yields an empty symbol index rather than an error. Verify at startup, fail loudly.
- Wire layer is llmwire. Nothing wire-shaped belongs here; a gap is a llmwire PR, a new model goes there first. Prices from its `profiles.yaml`, never derived from tokens.
- Models are llmwire profile ids, never hosts. Hosts and keys are `LLMWIRE_*`, never read by rongo.
- `embed.Model` is a constant of the build. A different one is a new database.

## Models
- **Model change is judged by `TestEvalMeasureAnswers`**, run twice. Retrieval numbers say what the answer was written from, never whether it was right.
- **Pro where a human reads: the answer. non-Pro + `ShortGate` everywhere else.** Bar is "output is an id or a label", not "doesn't think". `WithoutThinking`, `ShortGate`, `WithTemperature` are separate switches. Don't couple them.
- **Routing judge is non-Pro.** Two measurements went opposite ways (`2026-08-19-candidates.md`, `2026-09-06-routing-rerun.md`). No move back without a corpus and a number.
- **Pin `WithTemperature` on every call returning an id, label or decision.** Unpinned, the judge re-rolls between runs. Answer call stays unpinned; a person reads it.
- **Pinning narrows the re-roll, never removes it** — two pinned runs still differ by one question. Never conclude from a one-question gap. Run twice.
- **Never rank routing by accuracy.** Most questions want no card, so never-asking scores well and accuracy drifts every threshold toward silence. Price errors apart: needless card = 1, missed ambiguity = W (`2026-09-06-routing-cost-metric.md`).
- Cap every call with `WithMaxTokens` unless truncation is worse than length.
- **Retry once when nothing was delivered** — never a stream that delivered, a 4xx, a spent budget, a window run out.
- Embeddings cached by content hash. Never re-embed unchanged content.

## Invariants

### Answering
- **Never store or embed model text *about* code** — no module, file or symbol summary. Model prose written at index time goes stale like documentation and pulls a vector toward a claim no code honours. Reopen only with a `TestEvalMeasureAnswers` number, then embed-only, never shown. Per-turn judgement storing nothing (routing judge, reranker) is outside the rule.
- **Never invent.** Chain into non-indexed code → say so: call and configuration visible, internals not.
- **No hit means no hit** — "nothing found" plus the terms tried, never an answer from context.
- **Every claim citable**: repo, branch, file, line, indexed commit. Sources open in rongo's viewer at that commit, never a forge link. Cited files never evicted when capping context.
- **Templated answer reaches the browser once, whole, from `finishTurn`**, never from a lane — a lane streaming nothing leaves an empty answer until reload.
- **"What changed" comes from the commit lane, never the files** — the file index has no date, so "latest changes" ranked a month-old write-up over 33 commits of two days. Window and named repositories ARE the scope, applied by the pipeline, never the model. **Author stored, never served**: a shared page is public.
- **Release notes come from commits between two deployed versions, never the files.** Image DECLARED on the repository it is built from, never guessed. Overlays only, never `base/`. **An image yielding no range is one templated line** — never a guess, never a failed turn. **Direction from ancestry, never the order stages were named.** No rollback verdict: nothing declares which stage leads.
- **Code is truth, docs are context.** Contradiction is named, answer sides with code. Docs demoted by a decay, never filtered (`2026-09-05-doc-demotion.md`). All-docs turn says so and reports what the document states, not what the system does. Plans and mock-ups → `BACKEND_INDEX_EXCLUDE`, never a broad doc exclusion.
- **Data is filtered, docs are not** (`2026-09-17-data-file-cap.md`). Path-and-size rules re-applied at boot, so a new rule retires what an older build embedded. Body-only verdicts (secret, marker) are not.
- **Re-sweeping `DocDecay` reads r@20 AND r@5 AND mean rank** — membership at the cut cannot see a README going rank 1 → 8. **Mean rank over questions EVERY arm ranks**, never each arm's own hits: else an arm admitting one more at rank 19 reports a degradation that never happened.
- **Every answer opens with ONE sentence answering the question**, then explains. List only where the mechanism is a set. No headings — a short answer wearing three looks over-built.
- BA answer: core mechanism, three to five paragraphs, stop. Edge cases → follow-up. DEV gets inline code.
- **A diagram is a `mermaid` fence and does not cite.** One per answer; the introducing sentence carries markers. Labels in double quotes, no marker inside the fence — a bracket there is syntax, strict parser drops the whole picture. Trigger → target draws the target as ONE node; the renderer drew it twice and invented an order. A diagram that fails to draw goes into the corpus as `.txt` FIRST.
- **Diagram trigger is audience-neutral and positive** — a branch or second party earns the picture, in the reader's words, never as the fence. Negative phrasing drew 1 diagram in 19 BA answers; rewrite draws 13 (`2026-09-17-ba-diagrams.md`). Read prompt changes against that number, never a hunch.

### Threads
- **Thread is a record.** Follow-up adds an answer, never rewrites one. Correction is a new question.
- **One language per thread** — its first question's. Everything a person reads follows it, replays included. The record decides, not the request.
- **Answer prompt gets the previous QUESTION only**, never the previous answer — prose beside sources it was NOT written from is how a claim acquires a citation it was never read from. One turn back, never the thread.
- **Rework ("summarize", "as a table") answers from the previous answer and ITS OWN sources, no search.** First turn is never a rework. A basis missing even ONE chunk is refused — never summarised from survivors, never searched afresh; a fresh answer to "summarize" is a different answer dressed as a summary. Not "translate".
- **Recall reaches the model as ONE user message**, never a user/assistant pair — a prose assistant turn makes the model continue the conversation instead of returning JSON.
- **Thread is a funnel: narrows, never widens.** Pin is a ceiling; "in all repos" under a pin is not honoured. A named repo the pin excludes is reported as outside — silent refusal to widen is the same quiet drop the rule exists to stop.
- **Empty repo restriction means the whole corpus** — so a pin or chosen repo that no longer exists must FAIL the turn, never search. Has bitten twice.
- **Thread addressed by public id, never row number.** That id is not a share token: a share is revocable authorisation, an address is not.

### Memory
- **Standing instruction kept the turn it is said, in English, per reader.** About the READER only — never a claim about code, never language, audience or repo pin, never a one-off.
- **The block outranks the prompt**: overrides form, length, diagrams, what to mention. Never citing, inventing, "nothing found", language, audience. Empty memory leaves the prompt byte-identical — that is the eval baseline. Contradiction REPLACES, a rule never expires, one past the cap is refused, never fitted in by dropping one. Never on a shared page.

### Routing
- **Clarification answered once.** Choice starts a new turn, card becomes a record: collapsed, reopenable, inert. The answer closes it, so a failed turn leaves it open for retry.
- **Card offers PROJECTS, never repositories.** A project is a product — the one option an Analyst can tell apart. Backend-vs-ui is a *layer*, never a card. Naming a MEMBER narrows to it: a thread narrows, never widens.
- **Fold by project AFTER `repoCandidates`, never inside it** — dependency lookup joins on a repository name, a project name has no rows. The eval cannot catch it: its corpus is one project per repository.
- **`part`, `description`, `uses` declared, never inferred**, and reach the answer prompt. Members nothing calls are named too — that absence is the disambiguating half. Configuration: never cited, never presented as read from code.
- **A library is declared once under `libraries:`, member of every product using it.** First cut kept libraries out ("hops only") and a pinned thread reported its own library as "not searched, open a new thread".
- **A project turn is not a comparison of its own repositories.** Partial cover counts for nothing: a leftover repository drops the turn to repository-grained comparison.
- **A chosen card entry searches the members it STORED**, never a fresh lookup — a project gaining a member between card and click must not widen the reader's choice.
- **Clarify only on real ambiguity.** Candidates depending on each other are composition — answer all. **Any** named indexed repo → never a card, decided before anything is paid for. Two or more named is a comparison: one search per repo, answer covers each.
- **Never answer across repos the question did not ask for.** No repo named, candidates spanning two or more → repository card, deterministic, above margin and judge: a leader says which module scored best, never which product was meant.
- **More repos than a card fits → do not ask, make them narrow.** No model call, no "all repositories" way out — answering across all is what the rung refuses. Narrowing cap enforced server-side.
- **The card asks what the ROLE can answer.** Analyst only. Options tellable apart only by implementation, layer or package get no card. **Does not gate the repository card** — those are products, and gating would restore the cross-repo answer the rung below stops.
- **Choosing a repo re-searches it** — the one resume that searches again; a repo card grouped one fused list, and the runner-up's few chunks are not an answer.
- **A named repo the index lacks is said out loud.** Search drops the name so a mishearing cannot wipe the result. Turn carries a notice plus a prompt rule forbidding claims about it, stored, so resume and re-explain answer under the same rules.
- **Cross a repo boundary only with two reasons**: gathered code really references the symbol, target repo is indexed. Same hop budget.
- **A shared queue name or route is the third way across**, and a crossing does not count against the hop budget. Symbol walk stops short by `crossingReserve`: without it a Spring corpus fanned twenty hits to 141 sources at hop one and the crossing never ran (`2026-09-11-edges-in-gather.md`, `2026-09-11-declared-structure.md`).
- **Measured OFF, stay off**: route suffix matching (loose matches cross early, eat the reserve), whole-file reading, `response_format: json_object` on gate calls (`2026-09-13-json-object.md`). Knobs stay in code with those tables as the reason.
- **Fused list reranked by one short-gate call before the cut** (`2026-09-11-arms-after-the-crossing.md`, excerpt size `2026-09-16-rerank-excerpt.md`). Failed call or non-JSON reply keeps fused order — the reranker may never do worse than nothing, never fails a search. Raw question to the model, never an expansion.
- **Code-terms text has its own keyword rung** (`2026-09-16-code-rung.md`): AND rungs of guessed identifiers are expected to return nothing, so without it they entered fusion below the semantic lane. A second FTS column of paths and split identifiers was measured and dropped.
- **Test files come last within a hop.** The walk records how each source arrived; the answer prompt prints it.
- **Tests labelled, never excluded** (`2026-09-17-test-label.md`) — a test proves the mechanism, does not replace it. "How is this tested" is a real question. Exclusions go through `BACKEND_INDEX_EXCLUDE`.

### Repos and index
- **A structure edit is not a re-index.** `project`/`part`/`description`/`uses` leave the checkout alone. Reset trigger stays `clone_url`.
- **What a repository is BUILT from comes from manifests** — never docs, never a model. A documentation file goes stale without saying so.
- **Route extraction keeps BOTH class-level prefix and method path** — the class-level path is a route too; dropping it cost the flow corpus a part before the number caught it.
- **Repo list in `repos.yaml`, credentials never** — that file reaches a repo or ticket eventually. Tokens from `BACKEND_*`, injected at fetch time, never logged. Repos page is read-only status, no CRUD form.
- **`repos.Load` refuses** a malformed or ambiguous list: nameless, empty or repeated project; project sharing a name with a repository not its one member; unknown, self or cross-project `uses` that is not a library; library declared twice or reusing a project or repository name.
- **ctags runs from the temp directory on the bare file name** — universal-ctags hashes the path into every anonymous symbol's name, and that name is embedded, so a fresh temp path per run re-embedded hundreds of chunks of unchanged code and moved the measurement (`2026-09-16-stable-order.md`).
- **Never default a branch to `master`** — an omitted branch resolves the remote's default. Corpus is mixed: peeq/loom/rongo `master`, `ncruces/go-sqlite3` and `asg017/sqlite-vec` `main`.
- **One branch per entry**, named, so no two cards differ only by branch.
- **A configured branch vanishing upstream is a loud error on the Repos page**, never a silent stop — else the index freezes looking healthy.
- **A repo dropping out of `repos.yaml` is purged**; `enabled: false` parks instead. Purging by hand needs `purgeContent`'s per-file order — the FK cascade misses `chunks_vec`/`chunks_fts`. Floor: a list naming no repository is REFUSED, so a truncated file cannot wipe the corpus.
- **Checkout's `origin` is identity, directory name only a label.** A `clone_url` no longer matching resets and re-clones, else one repo's code answers under another's name.
- **INVALID `repos.yaml` REFUSES TO START; MISSING one boots.** On validation failure the previous list stays in the DB and the server serves a complete, confident, arbitrarily stale corpus — the Repos page shows only what the DB holds, so it cannot report the refusal. Cost nine hours once, surfacing as a `uses` arrow pointing the wrong way after a stray `7` on line 1. Missing is a different fact (first run, mount not up): nothing stale, warn and come up. Empty-but-present is INVALID.
- **No list loaded → DO NOT POLL.** The poller reads the active list from the DATABASE, so a missing file over a populated DB fetches the PREVIOUS list, refreshes every `last_run_at`, reports "Index current" for a configuration existing nowhere.
- **`last_run_at` is when the poller LOOKED, not when the index moved** — written on check and error alike. `last_sha` + counts say the index moved.
- **Log durations as `.String()`** — production is `slog.NewJSONHandler`, which renders a `time.Duration` as integer nanoseconds, so rounding is invisible.
- **`repository indexed` counts are whole-repo totals**, with `changed` beside them incrementally — `mode=incremental files=5000` reads as 5000 touched when one was.
- **`enabled: false` parks, repository OR project.** Parked = not polled, retrieved, a card candidate, a hop target, or on the Repos page. Index, checkout, EXISTING citations stay — parking stops new answers, never revises old ones, so thread sources and the source viewer do NOT filter on it; everything else does. **Vec lane filter goes in the `rowid IN (…)` subquery, never the join** — `chunks_vec` is top-k, so a join predicate post-filters and a parked repo silently eats the k slots.
- **`snapshot: true` is an extracted archive, not a clone** — no `clone_url`, `branch`, `token_env`; identity is the directory. Never cloned, fetched or polled. **`safe.directory` goes in the shared git runner, never at snapshot call sites** — the drop belongs to whoever unpacked it, git otherwise refuses EVERY command in it, breaking indexing and the source viewer. Missing or empty directory → loud error, never an empty index looking healthy. A directory holding a clone or mirror is refused. Purge takes the index, LEAVES the directory — rongo did not create it. A replaced drop has a new object store: missing `last_sha` → reset and full index, else the diff fails "bad object" forever.
- **A credential value never leaves the machine, by regex, not prompt.** In a configuration file, a value under a secret-named key or a credential-shaped value is replaced BEFORE selection, chunking, FTS, embedding and edge extraction; the source viewer applies the same function to its git read, so a citation cannot open the raw value. Key line stays, `${ENV}` placeholder stays. Secret manifests skipped whole. Source files NOT line-redacted — a `key = value` rule over Go would eat `password := os.Getenv(…)`. New shape → `redact_test.go` with an invented value.
- **Property key is an edge kind; a link is a token kind, never an edge.** Property: never JS/TS (`${cart.id}` is interpolation), never arbitrary yaml (every manifest would emit `spec.template.spec.containers`). Link: the navigation SITE, text as written, never resolved; markup gets the link branch only, never route rules. Traps on real markup: bare `location =` is React's useLocation, `xlink:href`/`<use`/asset paths are sprites.
- **A new extraction rule needs a forced re-index.**
- **Compare arms inside ONE database.** One database is deterministic — a re-index renumbering every chunk gathers the identical list, score for score — so a one-part move is real. Two FRESH indexes differ by the embedding endpoint's fourth-decimal drift over identical text (`2026-09-16-stable-order.md`). Say so when a number spans a rebuild.
- **Stages declared, never inferred.** A stage word that is a repository or project name, or an ordinary word (`integration`, `system`, `test`), is refused: "how is the X integration done" is a question about code, and narrowing to `intg/` answers from one stage while claiming the others were left out.
- **A stage narrows the turn like a named repository — per turn, never pinned.** Reader's own word wins; sources disagreeing means no narrowing. A repository declaring stages but not the asked one narrows to nothing; one declaring none is untouched.
- **A source under a declared stage directory is labelled from the prefix, never the model.** Code placeholder and the repository's defaults are the default; a stage file is the value that stage runs with. Report every stage by name — or, with a stage asked, that stage alone and that the others were not looked at.

### Sharing and routes
- **A share link exposes ONE thread, frozen where shared.** Later turns stay invisible until the owner raises the ceiling — a link never grows behind their back. Ceiling is the newest FINISHED turn, never the newest row.
- **Revoking is a flag, not a delete** — re-sharing returns the SAME link instead of stranding the one already sent. Shared page lists live links only: an audit, not a history.
- **Unknown, revoked and deleted are ONE 404.** Never an existence oracle. Public routes set `X-Robots-Tag: noindex`.
- **A shared page carries the thread's total tokens and cost; no per-turn usage, model name or follow-up.** Citations serve only what a turn under the ceiling cites. Only unauthenticated output path in the product.
- **A path with no page is a REAL 404**, never 200 and a shell rendering the unasked question. Thread addresses checked by SHAPE, never existence — answering would say which addresses are real.

## UI
- Warm Editorial dark; same `@theme` and fonts as ../loom and ../peeq. Reference: `docs/plans/rongo-ui-mock.html`.
- **Every page 900px, centred.** One cap and one centring keeps the pages' first lines on a single rule; change it in one place or it stops holding.
- Expandable → chevron, rotates 90° on open, **toward what it opened**. No triangle, plus/minus, glyph swap.
- **Scrollbar thumb OPAQUE, never rgba()** (../loom's `#383837`) — a translucent thumb composites over the panel behind it, so ONE declaration paints a different grey per surface. `getComputedStyle` reports the DECLARED colour, so a DOM sweep passes while the screen disagrees: check a rendered pixel. Global, because macOS hides overlay bars until you scroll and Windows always shows them.
- **Activity trace: one per turn, part of the record.** Stored per message, served with the thread, drawn rolled up when read back — a card returning without its ochre "your move" row is the record contradicting itself. **While the turn runs it is expanded with no toggle on screen** — progress is watched, never shut. **Every step reports what it found**, from facts the pipeline already held, no new model call. Never on a shared link.
- Ochre means "your move" — a waiting clarification, not the Done check. Once decided it loses the colour.
- **No caret on a streaming answer.**
- **A streaming answer follows the view until the reader takes it**; only the next turn re-arms, being back at the foot does not. Watch intent events (wheel, touchmove, pointerdown, a source opened), never the scroll event — a scroll lands after the next token already pulled the view back, so the browser's clamp is not the reader.
- **Sources pane answers to the audience of the turn it shows**: open for Developer, shut for Analyst, who got an explanation with no paths on purpose. Reader's click then wins for the rest of the thread. Chip on the source turn ONLY — the pane shows the newest citing turn, so a chip elsewhere opens somebody else's sources.
- Roles read "Analyst" and "Developer"; wire values stay `ba`/`dev`.
- **Everything a person reads follows the answer language.** Identifiers, paths, code, role names stay untranslated — a body naming "un Analyste" beside a button reading "Analyst" points at nothing. Chrome and model-internal calls stay English. Prompts name the language first AND last.
- **German is Swiss by string function, never prompt** (`backend/internal/ask/swiss.go`). Static German strings written `ss` by hand. Code never touched: fences, inline spans, identifier-shaped words. A prompt note asking for the same spelling was measured and moved nothing — do not add one back. `TestEvalMeasureAnswers` prints `digraphs N [words]`; read the words.
