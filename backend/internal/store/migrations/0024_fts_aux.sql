-- chunks_fts gains a second column, aux: the repo/path header, the symbol
-- breadcrumb and every camelCase/snake_case identifier of the chunk split
-- into words. unicode61 never splits AbandonedCartJob, and the header lived
-- only in the embedded text, so the keyword lane could not find a chunk by
-- its path or by the words inside its identifiers. Weighted below the source
-- column at query time (bm25(chunks_fts, 1.0, 0.5)). Deterministic
-- derivation, never model text.
--
-- aux cannot be rebuilt in SQL, so the table is recreated with column one
-- backfilled from chunks.raw_text and last_sha emptied, 0021's way: the next
-- poll runs full, every embedding hits the cache, and each file's row is
-- replaced with its aux as the run reaches it.
--
-- raw_text, not text: `text` is the ENRICHED form and would put the header
-- into the source column, where the measurement's baseline arm reads. Nothing
-- in `chunks` holds SearchText, so under BACKEND_INDEX_COMMENTS the transient
-- row carries the comments the operator stripped — for the minutes until the
-- forced full run replaces it with the stripped source and its aux.
DROP TABLE chunks_fts;
CREATE VIRTUAL TABLE chunks_fts USING fts5(raw_text, aux);
INSERT INTO chunks_fts (rowid, raw_text) SELECT id, raw_text FROM chunks;
UPDATE repo_state SET last_sha = '';
