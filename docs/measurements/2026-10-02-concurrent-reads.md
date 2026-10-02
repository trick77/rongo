# A turn's independent reads, side by side — tried, not merged

## What was tried

Four loops run independent reads in a row: the symbol walk's lookup per frontier source, the semantic and keyword lanes, a release turn's git calls per image, the router's cluster per hit repository. A helper ran each loop's reads at most four at a time and returned the results in the order of the inputs.

## The lists were identical

`TestEvalMeasureGathered` on the Go corpus (70 questions, six arms), one database, with `BACKEND_EVAL_GATHER_DUMP`:

| readers | wall time | lists |
|---|---|---|
| 1 (a plain loop) | 115 s | 420 |
| 4 | 101 s | the same 420 |

## Why it was not merged

**The gain is small.** 12% of search plus gather, about 30 ms a question on this corpus. A turn measured 114 s end to end (`2026-09-21-substring-rung.md`); the model calls are the turn. Keeping four idle connections instead of two moved nothing (100 s).

**The cost is not.** Found in review:

- A panic inside a read kills the process: the reads run on bare goroutines and no longer unwind into the HTTP recovery middleware.
- Four readers is per call, not per turn. A comparison turn already searches each named repository on its own goroutine; with four lanes each, six repositories is 24 connections, each its own wasm instance of SQLite, on a pool with no cap.
- The wrong failure can be reported: a git subprocess killed by the cancellation returns `signal: killed`, not `context.Canceled`, so the first error by position can be a consequence rather than the cause.

Fixing those means panic propagation, error classification per kind of read and a process-wide limit nested calls cannot deadlock on.

## If it is reopened

The release turn is where it should pay: its reads are git subprocesses. It was not measured — no corpus with declared stages and images was at hand. Neither was the flow corpus. Start there, and with a number.
