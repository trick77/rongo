# Documentation in the fused list: the mechanism, and the sweep that has to name the constant

**Status: the arm is written and the constant ships OFF (`DefaultDocDecay = 1.0`).
This document records the mechanism, the cohort change that makes the sweep
readable, and what the run has to show before the value is pinned. It does not
report numbers — the sweep needs a real embedding endpoint and a full corpus
index, and has not been run.**

## The problem

`AGENTS.md` states it and the answer prompt tells the model it: documentation
supplies intent, the code decides, a contradiction is named out loud
(`backend/internal/ask/answer.go`, `answerCommon`). The rule cannot fire on a
context that holds no code to disagree with, and four things conspire to
produce exactly that context.

1. **Retrieval is kind-blind.** The projection is `hitColumns`
   (`internal/retrieve/store.go`): repo, path, symbol, text, lines, sha.
   `files.lang` exists in the schema and is never selected. Neither
   `retrieve.Hit` nor `ask.Source` carries a doc flag.
2. **Both lanes favour prose.** `README.md` and `AGENTS.md` are dense domain
   vocabulary, which is what a natural-language question matches on in FTS5 and
   in the vector lane alike. The identifier lane is the one that finds code, and
   it only fires when the reader typed an identifier.
3. **Nothing ever hops out of a document.** Markdown yields no ctags symbol, so
   a doc chunk falls to a plain ~600-token line window with an empty `Symbol`
   (`internal/indexer/chunk.go`), and the reference walk joins the `symbols`
   table (`internal/ask/gather.go`). A doc-heavy hit list starves the walk as
   well as the cut — the two ways an answer reaches code.
4. **It cannot be trimmed back out.** `Gather` never evicts a search hit by
   budget, by design: an answer cites what it was built on. So a document that
   won fusion is guaranteed a place in the context.

The failure this produces is not "invention" and the invent-nothing rule does
not catch it: nothing is invented, the document really does say it — and may
have said it for a year while the code moved.

## The change

`IsDocPath` (`internal/retrieve/docpath.go`), path shape only, the same
discipline as `IsTestPath` and with the same care about what is left out: a bare
`.txt` (`requirements.txt`, `CMakeLists.txt`), `.sql` migrations, `.yaml`/`.json`
config. Demoting a contract is a worse mistake than keeping a document at full
standing.

Applied as a score decay in fusion (`FuseWeightedDecayed`), on the hit's own
`Score` and not only on the ordering key — the routing floor in `internal/ask`
reads that score, so a demotion the score hid would be a demotion that never
happened. A path that is both a test and a document takes the harsher factor
once, never the product.

A **demotion, not a filter.** `AGENTS.md` forbids the alternative outright:
"never a broad doc exclusion". A document that is the only thing matching still
wins; it just cannot outrank the code.

## The cohort had no downside axis

Swept as the cohort stood, the harshest decay would have won trivially: not one
of the 62 questions had a documentation file as its expected path, so nothing in
the measurement could ever fall as the demotion got stronger. A sweep with only
an upside axis names a filter, which is the thing that must not ship.

Three doc-led questions were added (`questions.json`, now 65), each verified
against the file:

| question | anchor | why it is doc-led |
|---|---|---|
| Which technical choices in rongo are locked? | `rongo AGENTS.md:11` | the answer exists only in the document |
| What has to be installed before rongo's dev environment starts? | `rongo README.md:27`, `docs/manual-verification.md:8-15` | the startup check in code names ctags alone, never the full list |
| Which attribute key does peeq use when it logs an error? | `peeq AGENTS.md:16` | the rule is written once; the practice is spread over every `slog` call |

The third is the sharpest of the three: document and code are about the *same*
fact, so a decay strong enough to hide the document answers from a call site
instead of from the rule.

`TestCohortHasADocLedAxis` runs without an embedding endpoint and fails if the
cohort ever loses that axis again.

## The sweep

`TestEvalMeasureDocSweep` in `backend/internal/retrieve/eval`, at the routing
cut (K = 20), sweeping `Retriever.DocDecay` over 1.0 / 0.7 / 0.5 / 0.35 / 0.2.
1.0 is measured in the same run rather than quoted, for the reason the
repo-diversity document gives: an arm compared against a remembered baseline
measures the memory.

Two numbers, pulling opposite ways:

- **code-led recall@20** — the number the demotion exists to raise.
- **doc-led recall@20** — the number that must not fall, and the reason a filter
  cannot pass.

**Re-freeze the expansions first.** Every arm reads `expansions.json` and fatals
on a missing entry, by design — the three questions added here have none, and
two others were already missing before them, so the diversity and routing arms
are blocked too until `TestExpandQuestions` has run. It carries the existing
file forward rather than replacing it, so nothing already paid for is lost.

It needs its own `-run`: `TestEval` does not match it.

```
... the same environment as below ...
go test -v -timeout 60m -run 'TestExpandQuestions|TestExpandQuestionRepos' ./internal/retrieve/eval/
```

Then the sweep, as the package doc describes:

```
BACKEND_EVAL=1 \
BACKEND_EMBED_BASE_URL=... BACKEND_EMBED_API_KEY=... \
BACKEND_EMBED_MODEL=text-embedding-3-small BACKEND_EMBED_DIM=1536 \
BACKEND_EVAL_DB=/tmp/rongo-eval-small.db \
BACKEND_REPOS_FILE=../../../../repos.yaml BACKEND_REPO_ROOT=/tmp/rongo-eval-repos \
go test -v -timeout 60m -run TestEval ./internal/retrieve/eval/
```

**The value to ship is the harshest decay that leaves doc-led recall intact.** A
decay that raises code-led recall by taking doc-led recall with it has not
improved retrieval; it has changed which questions rongo can answer. Record the
table here, pin `DefaultDocDecay`, and name this file in the constant's comment
the way `DefaultTestDecay` names its own.

## What ships in the meantime

The demotion is off, so retrieval is unchanged. What does ship is the half that
needs no measurement:

- documentation-only modules no longer reach the clarification card
  (`onlySupporting` in `internal/ask/route.go`, generalised from `onlyTests`),
- the answer prompt gains the case where there is nothing to contradict: a claim
  resting only on a document is reported as what the document states, and says
  the code was not among the sources,
- a turn whose sources are all documentation says so above the answer
  (`Scope.DocsOnly`), in the answer language, and the notice survives a resume
  and a re-explain.
