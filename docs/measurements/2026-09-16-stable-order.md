# Two full indexes of the same code now gather the same sources: ctags named anonymous symbols from a per-run path, and ties broke on chunk id

**Status: measured 2026-09-16 on the flow corpus. Before: two fresh indexes of unchanged code gathered different source sets on four questions (symbol walk 115 vs 119 sources on the order question, and so on), the spread AGENTS.md recorded as "re-index order". Cause: universal-ctags hashes the path it is handed into the name of every anonymous function, and `symbols.Extract` handed it a fresh temp path per run, so 286 of 3516 chunks carried a new symbol name, a new enriched text, a new content hash and a new embedding every time. After: chunk tables and content hashes are byte-identical across indexes, and a re-index that reassigns every chunk id gathers the identical list, score for score. What remains is the embedding endpoint itself: a fresh embedding of identical text differs in the fourth decimal.**

## Why

AGENTS.md line 98 said the crossing arm moved between 25 and 27 of 30 on the same code because "the walk's budget cut falls on a different chunk after every re-index (chunk ids, and so tie order, are assigned in index order)". The plan was to break every tie on the chunk's address (repository, path, start line) instead of its id. The two-index check that was to prove it found that the ids did not move at all: the indexer assigns them deterministically. What moved was the text.

## What was found

`ctags --_anonhash` shows the mechanism: `hooks.js` and `/tmp/abc/hooks.js` give different names for the same anonymous callback. `symbols.Extract` copied each file under `os.MkdirTemp` and passed the full temp path, so every index invented new `anonymousFunction<hash>` names. The name reaches `chunks.symbol`, the symbols table and the enriched text that is embedded (the breadcrumb line), which changes the content hash, misses the embedding cache, and re-embeds the chunk. On the flow corpus 286 of 3516 chunks (8 percent, the front-end's callbacks and the Java lambdas) were re-embedded on every full index, each time to a slightly different vector, and each time ranking a little differently.

## The changes

- `symbols.Extract` runs ctags with the temp directory as its working directory and hands it the bare file name, which is all it needs to infer the language and carries nothing that moves per run. Test: two extractions of a file with nested anonymous callbacks yield the same names.
- Ties break on the chunk's address, never its id: `lessByAddress` in fusion and in the blended reranker, `ORDER BY distance, repo, path, start_line` in the vector lane, `ORDER BY bm25(...), f.repo, f.path, c.start_line` in the keyword lane, `ORDER BY definers ASC, f.path, f.repo, s.name, c.ordinal` for the symbol walk (path first as before, repository and symbol name only as tie-breaks). The crossing landing already ordered by the chunk's ordinal, a file position, and was left alone.

## The measurement

Flow corpus at pin20260910, `TestEvalIndex` then `TestFlowGathered` without the reranker (the deterministic arms), the per-question hit and source lists diffed between databases.

| pair | what differs between the two databases | diff of the gathered lists |
|---|---|---|
| a, b: two fresh indexes, code before the fix | 286 chunks renamed and re-embedded | 4 questions on the symbol walk, 5 on the crossing arm, by 1 to 4 sources |
| c, d: two fresh indexes, code after the fix | chunk tables and hashes byte-identical; 1140 of 3488 cached vectors differ bitwise (the endpoint) | 2 lines: the guest-cart question, 150 vs 152 sources on the whole-file arm; hit order moves where a distance drifts in the fourth decimal |
| c, f: c copied, `last_sha` cleared, re-indexed from the cache (every chunk id reassigned) | ids only | empty, score for score |
| c, c: the same database twice | nothing | empty |

## What it says

- **The spread was the text, not the ids.** The address-stable ties are still right, and c against f is what they guard: a re-index that renumbers every chunk changes nothing the reader or the harness sees.
- **A full index no longer re-embeds unchanged code.** The rule "embeddings cached by chunk content hash; never re-embed unchanged content" was being broken on 8 percent of the corpus every run. Every deployment re-embeds its anonymous-symbol chunks once at the next full index; incrementally skipped files keep their old names until they change.
- **The floor is the embedding endpoint.** Two embeddings of identical text from text-embedding-3-small differ in the fourth decimal; two indexes with an empty cache will always differ by a hit order somewhere. Measurements that compare arms must share one database, which the harness already does; measurements that compare across rebuilds carry this floor and say so.
- Anonymous names are now unique per base name rather than per path, so two `index.js` files share a hash. Nothing follows an anonymous name (the walk's identifier set never contains one, and the nesting checks compare within one file), and the chunk tables of two fresh indexes are identical.
