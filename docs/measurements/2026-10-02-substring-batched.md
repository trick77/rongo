# Substring rung: all of a turn's terms in two scans

Follows `2026-09-21-substring-rung.md`, which measured the lane at 431 ms a turn at 25k chunks: one count and one fetch per term, twelve terms, 24 scans of `raw_text`.

## What changed

`Store.SearchSubstringsIn` takes every term of the turn. One scan counts each term and the scope; the hub guard is decided from those counts. A second scan reports which chunks hold the terms that are left. Only the chunks that survive each term's cut are read in full.

The result is unchanged. The per-term implementation is kept in the tests (`referenceSubstring`) and `TestSearchSubstringsIn_returnsPerTermWhatTheTwoScanRungDid` compares the two hit for hit over code, tests, docs, a hub term, a non-ASCII term, a parked repository, repository filters and a stage, at four cuts.

## Numbers

`BenchmarkSubstringLane`, ncruces wasm driver, same machine, same run:

| chunks | terms | per-term | batched |
|---|---|---|---|
| 10,000 | 12 | 134 ms | 82 ms |
| 25,000 | 12 | 339 ms | 199 ms |

The per-term figure is lower here than the 431 ms in the earlier document: another machine. Read the two columns against each other, not against that table.

## Two things the first attempts got wrong

**The scan count is not the cost; the fold is.** One scan with each term written as `instr(lower(c.raw_text), ?)` measured 397 ms against 421 ms: `lower()` copies the chunk and ran once per term per chunk either way. The inner `SELECT … lower(c.raw_text) AS folded … LIMIT -1` computes it once per chunk. `LIMIT -1` is load-bearing: without it SQLite flattens the subquery and puts `lower()` back into every term.

**Fetching rows to count them loses to counting.** One scan returning a row per matching chunk, counted in Go, measured 265 ms: the benchmark's filler holds a hub term, so most of the corpus crossed into Go to be thrown away by the guard. Counting in SQL first costs a second scan and is faster.

## What is left

Twelve `instr` calls per chunk per scan. `tokenize='trigram'` stays the named fallback if a larger corpus moves this.
