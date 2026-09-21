# A substring rung, for identifiers that occur only inside a larger token

**Status: the MECHANISM is measured and the cost is measured; the RETRIEVAL numbers are not. `TestEvalMeasureAnswers` needs an embedding endpoint and a built corpus, which this branch has not run. The rung ships behind `SubstringWeight`, on in `New`, off in a struct-literal `Retriever`, so the baseline arm is a field left alone. `WeightKeywordSubstring = 0.85` is a placeholder, NOT a swept value.**

## Why

A German question over the schadenmeldung corpus — "wie wird die Anzahl Kinder an Syrius übermittelt?" — answered that the mapping was not among the sources. The mapping is `ConverterEreignisregistrierung.java:162`:

```java
Optional.ofNullable(versicherter.getAnzahlKinder()).ifPresent(wsVersicherter::setAnzahlkinder);
```

The repository was indexed and contributed **0 of 40** fused hits.

`chunks_fts` is `fts5(raw_text)` with no `tokenize` clause (`0001_init.sql:128`), so unicode61 applies: `getAnzahlKinder` and `setAnzahlkinder` are each ONE token. The word "anzahlkinder" is inside both and equal to neither. The prefix rungs widen to the RIGHT (`query.go`, `minPrefixRunes`), and the word sits at each token's right end, so they cannot reach it either.

Casing is not involved. unicode61 case-folds, and `BuildFTSMatch` lowercases before matching; this was checked before the rung was written, because the first reading of the failure blamed the lowercase `k` in `setAnzahlkinder` and that reading was wrong.

## The mechanism, over the real line

Measured 2026-09-21 against the line above, in `substring_test.go`:

| query | FTS5 | substring |
|---|---|---|
| `anzahlkinder` (bare) | 0 | the chunk |
| `anzahlkinder *` (prefix rung) | 0 | — |
| `setanzahlkinder` (exact token) | 1 | the chunk |

Over the whole schadenmeldung corpus (21,952 chunks, 30MB, native sqlite3): FTS 0, substring 145 chunks.

**A caveat the test encodes.** The real chunk carries a German comment — "Element Anzahl Kinder ist in Syrius optional" — which spells both words bare, so a prose rung CAN reach that particular chunk through the comment. That is luck, not retrieval: the same converter with an English comment, or none, is invisible to every FTS rung. Code is truth, so the end-to-end test measures the code line without its comment.

## The corpus could not measure this before

`questions.json` held 65 questions. Four contain an identifier-shaped token (`DeadScanThreshold`, `FuseWeighted`, `ZeroBlob`, `SponsorBlock`) and **all four occur bare** in peeq/rongo/go-sqlite3, so FTS already finds them. There was no suffix-only case, and an eval run would have measured noise.

Five questions were added, each an identifier that occurs ONLY inside a larger token in production code. Baseline measured 2026-09-21 over 1,980 rongo Go chunks:

| question | real identifier | FTS | prefix | substring | in target file |
|---|---|---|---|---|---|
| `CanChoose` | `roleCanChoose` | 0 | 0 | 11 | 9 |
| `GateDeployment` | `ShortGateDeployment` | 0 | 0 | 15 | 4 |
| `FromClaims` | `CreateSessionFromClaims` | 0 | 0 | 7 | 2 |
| `MaxLandings` | `censusMaxLandings` | 0 | 0 | 8 | 2 |
| `FileTokens` | `WholeFileTokens` | 0 | 0 | 9 | 4 |

`FileTokens` is a struct field, the closest shape to the case that motivated the rung.

## Cost

`BenchmarkSearchSubstringIn` / `BenchmarkSearchKeywordIn`, through the ncruces wasm driver the product uses, Apple M3 Pro, one chunk in 500 carrying the identifier:

| chunks | substring scan | FTS lane |
|---|---|---|
| 1,000 | 1.9 ms | 2.3 ms |
| 10,000 | 20.8 ms | 21.8 ms |
| 25,000 | 56.1 ms | 54.1 ms |

The scan and the indexed lookup are within noise of each other: at this shape the join and row materialisation dominate, not the matching. A native sqlite3 figure over 21,952 chunks was 47 ms, so the wasm penalty is smaller here than assumed when the plan was written.

`tokenize='trigram'` stays the named fallback if a larger corpus moves this. It costs a mirror-managed virtual table, a migration and a full backfill, so it is not the first move.

## Decisions the code carries

- **Terms are derived in code, never asked of the model.** The understanding step guessed `KinderAnzahl` — the question's German noun order, reversed from the code's `anzahlKinder`. One more model call to recover from a model guess that missed is a second chance at the same mistake. `BuildSubstringTerms` pairs the question's own content words and folds the guessed `code_terms`.
- **Pairs are swept at gap 1 AND gap 2.** `stopwords` is English by construction and rongo answers German and French questions, so "Anzahl der Kinder" keeps its article as an ordinary content word. Adjacency is still emitted first, so the cap spends itself on words the question wrote side by side.
- **Length floor 8 runes**, well above `minPrefixRunes` (5). A prefix anchors a token's start; a substring anchors nothing, and "state" is inside half the corpus.
- **Hub guard at 2% of the corpus**, with a floor of 500 chunks below which it does not apply — a share over a handful of chunks is meaningless, and in a fixture the one chunk that legitimately holds the identifier IS a large share.
- **Address order, no bm25.** A scan has no ranking of its own. Ordering by occurrence count would rank fixtures and spec files above the converter, which mentions the field twice. Getting the chunk INTO the fused list is all this lane claims; `TestDecay` and the reranker rank from there.
- **`instr` rather than `LIKE`**: `%` and `_` in a term would otherwise be syntax needing an ESCAPE clause. Both sides folded in Go, because SQLite's `lower()` is ASCII-only.

## Still open

- `TestEvalMeasureAnswers` twice, per the model rule. Not run on this branch.
- r@20 AND r@5 AND mean rank over the questions EVERY arm ranks, inside ONE database.
- The sweep that settles `WeightKeywordSubstring`. 0.85 is reasoned (narrower than a prefix match, weaker than a whole-token match the reader typed), not measured.
