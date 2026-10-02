-- Two columns of chunks that every index run wrote and nothing ever read.
--
-- text was the enriched form of a chunk — breadcrumb, enclosing symbols, doc
-- comment, body — which is what gets EMBEDDED. The embedding is what is kept
-- of it: in chunks_vec, and in embed_cache under content_hash. The text
-- itself was stored beside raw_text, about doubling what a chunk's row holds,
-- and no query selected it. token_count likewise.
--
-- NO ROLLING BACK PAST THIS: a build from before it still inserts into both
-- columns, so on a migrated database every index write of that build fails
-- while its answers go on serving an index that no longer moves.
--
-- One way: the enriched text comes back only by indexing again. Nothing that
-- is stored depends on it — a re-index recomputes it, hashes it, and finds
-- the vector in the cache.
ALTER TABLE chunks DROP COLUMN text;
ALTER TABLE chunks DROP COLUMN token_count;

-- Five indexes that repeat the leftmost column of the UNIQUE constraint on
-- their own table. SQLite already keeps an index for each of those
-- constraints and answers these lookups from it; the copies cost a second
-- b-tree update on every insert and delete, on the tables an index run and a
-- turn write most.
--
--   files(repo)                 under UNIQUE (repo, path)
--   chunks(file_id)             under UNIQUE (file_id, ordinal)
--   messages(thread_id, …)      the same columns as UNIQUE (thread_id, ordinal)
--   citations(message_id)       under UNIQUE (message_id, marker)
--   message_sources(message_id) under UNIQUE (message_id, chunk_id, commit_id)
DROP INDEX IF EXISTS idx_files_repo;
DROP INDEX IF EXISTS idx_chunks_file;
DROP INDEX IF EXISTS idx_messages_thread;
DROP INDEX IF EXISTS idx_citations_message;
DROP INDEX IF EXISTS idx_message_sources_message;
