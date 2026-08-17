# What reaches the answer, measured on a catalogue that can carry the question

Two measurement documents end with the same two sentences: retrieval recall is
the wrong lens, because the answer is written from search **plus** the reference
walk; and at 28 questions a single question is 3.6 points, so nothing small is
readable. This closes both.

Same corpus, same index (`text-embedding-3-small`, 1536, `/tmp/rongo-eval-small.db`),
**not re-indexed** — peeq 523 files / 5685 chunks, loom 530 / 3855, go-sqlite3
292 / 2186.

## The catalogue

61 questions, up from 28. The 33 new ones were written the other way round:
read the indexed file first, then write the question it answers, and record in
the question record the line that was read. Nothing is guessed.

Provenance therefore starts with the new questions: the 33 added here carry a
`verified` line naming what was read, the 28 inherited ones do not, because
their expected paths were established in phase 2 and only their *ambiguity* was
re-checked here. The file is knowingly two-tier on that field.

The record shape changed, because the old one could not say what phase 4b will
be graded on. `expect_repo` plus `expect_paths` cannot express «either-or, and
asking is the right reaction». Every question now carries a list of candidates
and a `resolution`:

| resolution | n | meaning |
|---|---|---|
| `unique` | 44 | one place answers it |
| `ambiguous` | 12 | two independent places answer it; the right reaction is to ask which |
| `composition` | 5 | two places are parts of one mechanism; asking would be wrong |

The ambiguous ones are mined from the seven packages peeq and loom both have —
`httpapi`, `store`, `rag`, `llm`, `auth`, `config`, `sse`. No repository was
added for them. The composition ones run along the only genuine cross-repository
dependency in the corpus: peeq and loom both declare `ncruces/go-sqlite3 v0.23.3`
in `go.mod`, and that repository is indexed.

### Two of the 28 were silently wrong

The re-check the phase-4a document asked for found two questions scored as
having one right answer that do not have one:

- **«Wie wird die Anzahl Tokens eines Chunks geschaetzt, ohne einen Tokenizer
  herunterzuladen?»** — `estimateTokens` exists character-for-character in
  `peeq/backend/internal/rag/chunk.go:38` and
  `loom/backend/internal/rag/chunk.go:32`, comment included.
- **«Wie meldet sich ein Benutzer per OIDC an?»** — `auth/oidc.go` in both, the
  same `OIDCBackend` interface, differing in the cookie name.

Both are now `ambiguous`. The other 26 survive the check: either the question
names its product, or the subject exists only once (yt-dlp, AirPlay, thread
titles, artifacts, RRF fusion, jittered loops, filename sanitising — each
verified by grepping the other repository).

### The anchor

Reclassifying breaks comparability, so one arm scores the original 28 texts
against their original first candidate. It must reproduce the published numbers,
and it does:

| arm | recall@5 | recall@20 | MRR |
|---|---|---|---|
| **anchor: the original 28** | **0.679** | **0.786** | **0.476** |

That line is what connects everything below to the two earlier documents. Had it
come back different, nothing here would be comparable to anything published
before it.

## Result 1: retrieval recall understates the product by 20 points

The new arm runs what the pipeline runs — `retrieve.Search` at K=20, then
`ask.Gatherer.Gather` with the deployed bounds (2 hops, 24000 tokens) — and asks
whether the expected file is among the **sources**, not among the hits.

Unique cohort, n=44:

| arm | in the gathered set | reached by the walk | mean sources |
|---|---|---|---|
| raw question, 0 hops (control) | 0.750 | 0 | 20.0 |
| raw question + walk | 0.818 | 3 | 103.4 |
| **expanded question + walk (the product)** | **0.955** | **3** | **110.3** |

**The control arm reproduces recall@20 = 0.750 exactly**, as it must: at zero
hops `Gather` returns the search hits and nothing else. That is the wiring
check, not a finding — a mismatch there would have meant the harness was
measuring something other than the pipeline.

The finding is the last row. **What the search returns is 0.750. What the answer
is written from is 0.955.** Every number in the two earlier documents describes
the first figure and was read as if it described the product.

`playbackgrant/store.go` is the case that motivated this arm, and it reproduces:
the search misses it at K=20 in every arm, and the walk gathers it at hop 1.

### The guard against an arm that cannot fail

Two hops and a 24000-token budget could pull in enough of the corpus that
everything looks found. It does not: the mean is 110 sources out of 11726
chunks, roughly 1 %. And two unique questions are still missing after the walk —
«Ab wann gilt ein Video als altes Material» (`scan/scheduler.go`) and «Warum
bekommt ein SVG kein Vorschaubild?» (`artifact/thumbnail.go`). An arm that
cannot fail would not have those.

But the cost is real and belongs next to the number: the walk more than
quintuples the material, from 20 sources to 103, for three additional questions.
That is 83 extra chunks per question to gain 7 points of gathered recall, and
what limits it is the token budget rather than the hop count.

## Result 2: query expansion is a clear win at n=61

The phase-4a document reported «three gained, three lost» and refused to call
it. On 61 questions it is not close:

| arm | recall@5 | recall@20 | MRR |
|---|---|---|---|
| raw question | 0.557 | 0.721 | 0.406 |
| **question + expansion** | **0.639** | **0.869** | **0.515** |

**Nine questions go from missing to found, and none is lost.** Not one question
that had a rank drops out of the top 20 — the failure mode that made the n=28
run unreadable does not appear at all here.

Rank movement is still noisy in both directions (`Kanalfilter` 5 → 12,
`Metadaten-Vorpruefung` 11 → 17, against `Share-Links` MISS → 14,
`Threadliste` MISS → 19, `Port` 14 → 6). That is the same reshuffling phase 4a
saw. What changed is that the sample is now large enough for the gains to
outweigh it instead of cancelling with it.

The verdict on phase 4a: **the expansion earns its call.** It was the right
build; the question set was too small to say so.

The failure mode from the phase-4a document is unchanged and still visible in
`expansions.json` — the model guesses the vocabulary of the technology it
assumes. «Wie kommt ein Apple TV ohne Anmeldung an die Mediendatei?» produced
`AVPlayerViewController`, `MPNowPlayingInfoCenter`, `NSFileCoordinator`; the
question about parked videos produced `AVPlayerItem` and `isPlaybackLikelyToKeepUp`.
peeq is a Go service. The business-language lane is what carries these, not the
code lane.

## Result 3: the clarification usually has material to work with

Ambiguous cohort, n=12, «at least two of the alternatives are present»:

| arm | two or more alternatives | candidates found |
|---|---|---|
| search alone (top 20) | 0.667 (8/12) | 18/24 |
| raw + walk | 0.750 (9/12) | 20/24 |
| **expanded + walk** | **0.833 (10/12)** | **21/24** |

So in ten of twelve cases phase 4b will have both alternatives in front of it
and can ask which is meant. The other two fail in opposite ways, and only one of
them is the failure this cohort exists to expose:

- **«Auf welchem Port hoert der Dienst, wenn nichts konfiguriert ist?»** gathers
  loom's `config.go` and not peeq's. This is the real case: rongo would answer
  confidently from one of two equally right places — and it would even give the
  *correct value*, because both default to `:8080`. A reader has no way to tell
  that the other half exists. That is the quiet version of the failure, not the
  loud one.
- **«Wie lang darf eine einzelne Zeile im Antwortstrom des Modells hoechstens
  sein?»** gathers neither `llm/stream.go`. Nothing is found, so rongo says
  nothing was found. That is the invariant working, not a wrong answer.

**This is not a recall number and must not be read as one.** It says nothing
about whether rongo *does* ask — nothing asks today. It says whether the
material for asking is on the table.

## Result 4: cross-repository composition does not assemble, and that is the phase-4b finding

Composition cohort, n=5, «all parts present»:

| arm | all parts | parts found |
|---|---|---|
| search alone (top 20) | 0.000 (0/5) | 1/10 |
| raw + walk | 0.000 (0/5) | 4/10 |
| **expanded + walk** | **0.200 (1/5)** | **6/10** |

One of five. And the pattern in the per-question detail is consistent: the
go-sqlite3 half is gathered, the peeq or loom half is not, or the reverse — the
two halves are almost never present together.

Two mechanisms, both visible:

**The reference walk cannot cross this boundary.** `Gatherer.referenced` follows
a name only when the gathered text mentions it and at most four files define it
(`maxDefiners`, `gather.go:38`, applied at `gather.go:172`). The seam here is
`sql.Open("sqlite3", dsn)` and a blank
import — neither is a symbol hop. The design's rule that a repository boundary
is crossed only when the gathered code really references the symbol is exactly
what blocks it: the code references a *string*.

**`repo_deps` does not exist.** The spec's hard signal for telling composition
from ambiguity is a dependency-manifest table, and it is unbuilt. Both `go.mod`
files declare the dependency; nothing reads them.

These five questions were kept in the catalogue **although the harness scores
them near zero.** Dropping them would have been curating the question set to
what the measurement already finds, which makes the metric unfalsifiable. They
are verified compositions in the code; that they are not assembled is the
result.

### What this cohort cannot say

The corpus has exactly **one** cross-repository dependency edge, so all five
composition questions run along the same seam — a database driver. A different
kind of dependency (a shared library with real function calls across the
boundary) might behave completely differently, and this set cannot tell.
Widening it means indexing a repository pair that genuinely calls into each
other, which is a corpus change, not a question-set change.

## Reproducing

```
BACKEND_EVAL=1 BACKEND_EVAL_DB=/tmp/rongo-eval-small.db \
BACKEND_EMBED_BASE_URL=... BACKEND_EMBED_API_KEY=... \
BACKEND_LLM_BASE_URL=... BACKEND_LLM_API_KEY=... \
BACKEND_EMBED_MODEL=text-embedding-3-small BACKEND_EMBED_DIM=1536 \
BACKEND_REPO_ROOT=/tmp/rongo-eval-repos \
go test -v -timeout 120m \
  -run 'TestExpandQuestions|TestEvalMeasure$|TestEvalMeasureExpansion$|TestEvalMeasureGathered$' \
  ./internal/retrieve/eval/
```

`TestExpandQuestions` costs one short-gate call per question and rewrites
`expansions.json`; the other three need only the embedding endpoint. The index
is not rebuilt.

One limitation of the gathered arm that cannot be fixed here: `expansions.json`
records the understanding step's *texts* but not its `Repos`, so the pipeline's
repository filter is not reproducible in the harness. Every arm here searches
all three repositories.

## When to revisit

The question set is now large enough that a difference of 0.05 is three
questions rather than one and a half, and both remaining recommendations are
about the corpus, not the count:

- A second dependency edge with real cross-repository calls, so the composition
  cohort measures more than one seam.
- The two unique questions the walk still misses are both "a constant explained
  in a comment three levels down a large file" — the shape the chunker is
  weakest at, and the next thing worth a measurement of its own.
