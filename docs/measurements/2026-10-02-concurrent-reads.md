# A turn's independent reads, side by side

## What changed

Four loops ran independent reads in a row: the symbol walk's lookup per frontier source, the semantic and keyword lanes, a release turn's git calls per image, the router's cluster per hit repository. `sched.Ordered` runs each loop's reads at most `sched.Readers` (4) at a time and returns the results in the order of the inputs.

## The lists are identical

`TestEvalMeasureGathered` on the Go corpus (70 questions, six arms), one database, with `BACKEND_EVAL_GATHER_DUMP` writing every search hit with its score and every gathered source with its hop and reason:

| readers | wall time | dump |
|---|---|---|
| 1 (a plain loop) | 115 s | `41588e9d…` |
| 4 | 101 s | `41588e9d…` |

420 lists, byte for byte the same. One reader is the loop the code had: `Ordered` with a limit of one runs the items in order on the calling goroutine.

## The gain is small

12% of search plus gather, which is about 30 ms a question on this corpus. A turn measured 114 s end to end (`2026-09-21-substring-rung.md`); the model calls are the turn. Keeping four idle connections instead of two moved nothing (100 s).

What it costs: up to four connections per turn while the reads run, and each connection is its own wasm instance of SQLite.

## Not measured

- The flow corpus (`TestFlowGathered`). Its database and clones lived under `/tmp` and are gone; rebuilding is eight clones and a full embed. The crossing passes are not touched by this change — only the symbol walk's lookups are — but the proof above is the Go corpus alone.
- The release turn. Its reads are git subprocesses, where four at a time should pay more than it does for SQLite. No corpus with declared stages and images is at hand.
