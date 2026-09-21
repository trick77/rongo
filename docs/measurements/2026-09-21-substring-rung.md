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

**Per TERM, not per turn.** The lane issues one query per generated term, and each does two scans — the hub-guard count and the fetch. The number that matters for a turn is `BenchmarkSubstringLane`:

| chunks | terms | per turn |
|---|---|---|
| 10,000 | 12 | 172 ms |
| 25,000 | 12 | 431 ms |

431 ms against a turn that measured 114 s end to end. Tolerable, and NOT "within noise of the indexed lane" — that claim holds per term only, and the first version of this document let it read as if it held per turn. Read the weight sweep against 431 ms, not 56 ms.

`tokenize='trigram'` stays the named fallback if a larger corpus moves this. It costs a mirror-managed virtual table, a migration and a full backfill, so it is not the first move.

## Decisions the code carries

- **Terms are derived in code, never asked of the model.** The understanding step guessed `KinderAnzahl` — the question's German noun order, reversed from the code's `anzahlKinder`. One more model call to recover from a model guess that missed is a second chance at the same mistake. `BuildSubstringTerms` pairs the question's own content words and folds the guessed `code_terms`.
- **Pairs are swept at gap 1 AND gap 2.** `stopwords` is English by construction and rongo answers German and French questions, so "Anzahl der Kinder" keeps its article as an ordinary content word. Adjacency is still emitted first, so the cap spends itself on words the question wrote side by side.
- **Length floor 8 runes**, well above `minPrefixRunes` (5). A prefix anchors a token's start; a substring anchors nothing, and "state" is inside half the corpus.
- **Hub guard at 2% of the corpus**, with a floor of 500 chunks below which it does not apply — a share over a handful of chunks is meaningless, and in a fixture the one chunk that legitimately holds the identifier IS a large share.
- **Kind first, then address; no bm25 and no SQL `LIMIT`.** A scan has no ranking of its own, and address order ranks a repository's LAYOUT rather than its relevance. Measured on the motivating corpus: 175 matching chunks, and the converter sits at position **35** of a 40-row limit, behind `.puml` entity diagrams and persistence fixtures that merely NAME the field — five slots from being cut by a directory name. The lane now takes every match (the hub guard has already bounded the set), orders code before tests before documentation, and cuts afterwards. Cost unchanged: 56ms at 25k chunks either way.
- **Two budgets, not one cap.** Guessed `code_terms` get 4 of 12 slots, prose candidates the rest. A single shared cap of 6 was measured to starve the motivating question outright: the step guessed `schadenmeldung`, `kinderanzahl`, `übermittlung`, `datenübertragung` — none of them the identifier — and those four plus two leading pairs filled it, cutting `anzahlkinder` at position 9. The rung exists for the questions where the guess MISSED, so the guess must not spend the budget that recovers them.
- **Single content words, after the pairs.** A question that IS one identifier has no pair to form, and every `identifier`-kind question in the eval corpus is that shape — `BuildSubstringTerms("CanChoose", nil)` returned nothing before this, so the five questions added to measure the lane could not reach it. They come last because a lone word is the weaker claim: "versicherter" is half the corpus, "anzahlkinder" is a mapping.
- **`instr` rather than `LIKE`**: `%` and `_` in a term would otherwise be syntax needing an ESCAPE clause. Both sides folded in Go, because SQLite's `lower()` is ASCII-only.

## Found in review, fixed

- **One lane for every term, not one per term.** `get<X>`, `set<X>` and `<X>` routinely all match the same chunk — it is what code terms plus word pairing PRODUCE. As separate lanes that chunk scored 3 × (0.85/60) = an effective weight of **2.55**, above `WeightKeywordStrict` (1.0), from a rung deliberately placed below it. And `Hit.Lanes` dedups the NAME, so the hit reported one lane while carrying the score of three — the activity trace and any lane-based measurement would have misattributed the movement. Hits are now deduped by `ChunkID` into a single lane. The regression test was checked by reverting the fix: it reads 0.028333 against a one-lane 0.014167.
- **The hub guard's denominator now carries the same filters as its numerator.** It counted matches against every chunk in the database while the numerator was scoped, so one large parked or out-of-scope repository permanently disarmed the guard for every live one — the mistake the vec lane's `rowid IN (…)` rule exists to stop.
- **snake_case spellings are emitted.** The haystack is raw source: a corpus writing `set_anzahl_kinder` contains no run of letters spelling `anzahlkinder`, so no case variant of the glued needle could find it. The rung was silently dead over Python, Rust, C and Ruby.

## Found in a second review, fixed

- **A wide early term evicted the narrow one's hits.** The lane took each term's hits in turn and cut at `candidates`, so lane rank was decided by TERM ORDER — and code terms run first, being by construction the guesses that missed. Measured on the motivating question: the guessed term `schadenmeldung` returned 40 hits on its own, filling the lane while staying under the hub share, and `anzahlkinder` with its single chunk — the mapping the rung exists to recover — **was evicted entirely**. The rung failed on the exact question it was built for. Now round-robin across terms: a wide term costs itself, not the lane. Converter goes from absent to lane rank 1.
  - The earlier test proved only that `anzahlkinder` was IN the terms list, not that its hits survived the lane. A term-list assertion is not a retrieval assertion.
- **A correctly guessed separator code term was destroyed by folding.** `set_anzahl_kinder` folded to `setanzahlkinder`, which cannot occur in a source that writes the underscores; same for `max-retry-count` and `com.acme.Converter`. The rung was dead exactly when the model guessed RIGHT in a separator language — the opposite of the failure it was built for. A code term already carrying separators is now emitted in both spellings.
- **`texts[0]` guarded and its contract named.** `Query.Texts` documents the raw question as its first entry, while `Question` is deliberately empty on the multi-repo and chosen-repo paths, so reading `Question` would have switched the prose half off there while the lane still reported itself as running.
- **The "two budgets" claim corrected to what the code does.** Both sources share one counter, so `maxSubstringCodeTerms` is a CEILING on the guesses (at most 4 of 12), not a reserved allocation for prose. Right shape, wrong description.

## Known limit, measured and left open

A non-ASCII letter MID-identifier followed by an internal capital is unreachable: `getÜbermittlungKinder` with needle `übermittlungkinder` returns 0. SQLite's `lower()` is ASCII-only, so the column cannot be folded over the umlaut, and the two spellings tried cover only a leading capital. The ASCII control (`kinder`) matches, so the lane is not broken — it is bounded.

Closing it means folding the HAYSTACK: a generated folded column (a migration plus a full re-index) or a Unicode `lower()` registered on every connection. Both are larger than this rung and neither should be decided without a number. Stated in the code at the match site so the next reader does not rediscover it.

## Retrieval, measured 2026-09-21

Corpus pinned to `pin20260820` (peeq `bb04021`, rongo `0b989b6`, go-sqlite3 `3fa3f30`). **Files reproduce exactly — 526 / 142 / 293 — chunks do NOT: 5684 / 1217 / 2137 against the recorded 5694 / 1358 / 2180.** Same code, different chunking: the data-file cap and the extraction rules landed after 2026-08-20. So these numbers compare **arm against arm inside this one database** and must not be read against the older tables.

`TestEvalMeasureFTS`, three arms in one run, deterministic (no reranker, no model call). Mean rank over the 41 questions every arm ranks.

| arm | r@5 | r@20 | MRR | gathered | composition | mean rank |
|---|---|---|---|---|---|---|
| prose floor only | 0.787 (37) | 0.872 (41) | 0.713 | 0.894 (42) | 4/5 | 2.44 |
| code rung 0.8, no substring | 0.809 (38) | 0.872 (41) | 0.728 | 0.894 (42) | 4/5 | **2.10** |
| + substring 0.40 | 0.787 (37) | 0.894 (42) | 0.715 | 0.915 (43) | 4/5 | 2.24 |
| + substring 0.60 | 0.787 (37) | 0.894 (42) | 0.716 | 0.915 (43) | 4/5 | 2.22 |
| + substring 0.70 | 0.787 (37) | 0.894 (42) | 0.716 | 0.915 (43) | 4/5 | 2.27 |
| **+ substring 0.85 (ships)** | 0.766 (36) | **0.915 (43)** | 0.698 | **0.936 (44)** | **5/5** | 2.39 |

0.40 to 0.70 are one plateau: identical recall, mean rank within 0.05. **0.85 is the only weight that recovers the second question and fixes composition.**

What it moves, question by question (0.85 against the same arm without the rung):

- **Recovered from nothing:** `MaxSources` 0 → 7 (the suffix-only case the rung exists for), "from when on does a video count as old material" 0 → 17, and the composition question "are foreign key constraints active" completing 4/5 → 5/5.
- **Cost:** eight already-working questions slip, six by a single place (11→12, 1→2, 4→5, 2→3, 13→14). Two slip further: "how is it recognised that one repository uses another" 1 → 4, "which attribute key does peeq use when it logs an error" 5 → 9. That is the r@5 loss and most of the mean-rank move.

Trade accepted deliberately: the rung's job is to reach what no FTS rung can see at all, and a one-place slip on a question already answered is cheaper than a question that cannot be answered. Revisit if the reranked path disagrees.

## Corrected: the first eval questions measured nothing

The five questions added with the rung (`CanChoose`, `GateDeployment`, `FromClaims`, `MaxLandings`, `FileTokens`) were picked by scanning **today's** rongo. Four of their identifiers **do not exist in the pinned corpus** — `censusMaxLandings`, `WholeFileTokens`, `roleCanChoose` and `CreateSessionFromClaims` all postdate 2026-08-20, and `census.go` does not exist there at all. They were unanswerable by construction, not hard, and scored rank 0 in every arm.

Replaced with four verified at the pin, each 0 bare occurrences and exactly one production file:

| question | identifier | repo | file |
|---|---|---|---|
| `MaxSources` | `answerMaxSources` | peeq | `backend/internal/httpapi/answer_handlers.go` |
| `CandidateIdx` | `FromCandidateIdx` | rongo | `backend/internal/threads/store.go` |
| `ByModule` | `RerankByModule` | rongo | `backend/internal/retrieve/rerank.go` |
| `FormatJulianDay` | `TimeFormatJulianDay` | go-sqlite3 | `time.go` |

`GateDeployment` survives from the first set. The lesson is general: **an eval question must be verified against the CORPUS THE EVAL INDEXES, never against the working checkout.**

Of these, only `MaxSources` actually needed the rung; the other three reach rank 1 through the prose lanes because their expansions happen to contain words the corpus spells bare. A question being suffix-only makes it *reachable only by substring in the keyword lane* — it does not make the semantic lane blind.

## Answers, measured 2026-09-21

`TestEvalMeasureAnswers` runs against the **flow corpus** (Sock Shop), not the Go corpus — `hack/run-flow-eval.sh`, per the header of `answers_test.go`. Pointing it at the Go corpus answers Sock Shop questions out of peeq/rongo and scores 0/N on every rubric; that was done once here by mistake and the numbers discarded.

Two runs, `BACKEND_EVAL_ANSWER_RUNS=2`, rung on at 0.85, BA audience, rerank pool 60, 800-rune excerpts:

| run | rubric present | contradicted | forbidden | cited parts | asked | failed | diagrams |
|---|---|---|---|---|---|---|---|
| 1 | 18/22 | 0 | 0 | 15/23 | 0 | 2 | 5 |
| 2 | 27/30 | 0 | 0 | 18/30 | 0 | 0 | 7 |

**The denominators differ because run 1 lost two questions to model-delivery failures** — one `finish_reason=length` at 16384 completion tokens, one empty reply after 700 — so their rubric parts left the total. Both are "nothing was delivered" cases, retry material under the model rule, not retrieval results. Run 2 delivered every question.

What the two runs agree on, which is what this arm was run to check: **0 contradicted and 0 forbidden in both**, and 0 asked-back. The rung does not make an answer claim something the code does not say, and it does not push the routing into needless clarification. Run 2's 27/30 present on a complete set is the cleaner figure.

This corpus contains no suffix-only question, so it measures whether the rung HARMS answer quality, never whether it helps. The help is the retrieval table above.

## Still open

- The reranked path (`TestEvalMeasureRerank`), where the eight small rank slips may or may not survive the rerank.
- A flow-corpus question that is genuinely suffix-only, so the answer arm can price the rung's benefit rather than only its cost.
