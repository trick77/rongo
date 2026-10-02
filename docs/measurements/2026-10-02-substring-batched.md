# Substring rung: all of a turn's terms in two scans

Follows `2026-09-21-substring-rung.md`, which measured the lane at 431 ms a turn at 25k chunks: one count and one fetch per term, twelve terms, 24 scans of `raw_text`.

## What changed

`Store.SearchSubstringsIn` takes every term of the turn. One scan counts each term and the scope; the hub guard is decided from those counts. A second scan reports which chunks hold the terms that are left. Only the chunks that survive each term's cut are read in full. All three statements run in one read transaction — one WAL snapshot — so a poll re-indexing a file between them cannot take its chunks out of the lane after the cut.

`Store.SearchSubstringIn`, the single-term call, is unchanged and still what the locate loop's grep uses: for one term it is the faster of the two.

The result is unchanged. `TestSearchSubstringsIn_returnsPerTermWhatThePerTermRungDid` compares the batch against the single-term call hit for hit over five scopes and four cuts, on a corpus sized so the lists are not empty: 25 hits per identifier across code, tests, documentation and a stage directory, a hub term that is skipped, a non-ASCII identifier in both spellings, a parked repository. `…_theComparisonIsOverListsThatHoldSomething` pins those counts.

## Numbers

ncruces wasm driver, same machine, same run.

`BenchmarkSubstringLane`, one turn:

| chunks | terms | per-term | batched |
|---|---|---|---|
| 10,000 | 12 | 136 ms | 80 ms |
| 25,000 | 12 | 339 ms | 201 ms |

`BenchmarkSearchSubstringIn`, one term:

| chunks | per-term | batched |
|---|---|---|
| 25,000 | 48 ms | 60 ms |

The per-term figure is lower here than the 431 ms in the earlier document: another machine. Read the columns against each other, not against that table.

## What the first attempts got wrong

**The scan count is not the cost; the fold is.** One scan with each term written as `instr(lower(c.raw_text), ?)` measured 397 ms against 421 ms: `lower()` copies the chunk and ran once per term per chunk either way. The inner `SELECT … lower(c.raw_text) AS folded … LIMIT -1` computes it once per chunk. `LIMIT -1` is load-bearing: without it SQLite flattens the subquery and puts `lower()` back into every term. The fold is left out when no term reads it.

**Fetching rows to count them loses to counting.** One scan returning a row per matching chunk, counted in Go, measured 265 ms: the benchmark's filler holds a hub term, so most of the corpus crossed into Go to be thrown away by the guard. Counting in SQL first costs a second scan and is faster.

**The batch is not free for one term.** The subquery and the id read cost more than they save when there is nothing to share, which is why the single-term call was put back.

## What is left

Twelve `instr` calls per chunk per scan. `tokenize='trigram'` stays the named fallback if a larger corpus moves this.
