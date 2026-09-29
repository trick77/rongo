-- A source is recorded by what it IS — repository, path, the commit it was
-- read at, the lines — not only by the row it came from. chunk ids are not
-- stable: a poll that touches a file re-inserts every chunk of it under new
-- ids, so a record keyed on them lost its basis a minute after the answer
-- and a rework of it was refused. The commit does not move; the record
-- re-reads the lines from git there.
--
-- chunk_id and commit_id stay: the uniqueness is keyed on them, and a row
-- the backfill below cannot complete still resolves the old way.
-- For a commit source sha is the commit, path and lines stay empty.
ALTER TABLE message_sources ADD COLUMN repo TEXT NOT NULL DEFAULT '';
ALTER TABLE message_sources ADD COLUMN path TEXT NOT NULL DEFAULT '';
ALTER TABLE message_sources ADD COLUMN sha TEXT NOT NULL DEFAULT '';
ALTER TABLE message_sources ADD COLUMN start_line INTEGER NOT NULL DEFAULT 0;
ALTER TABLE message_sources ADD COLUMN end_line INTEGER NOT NULL DEFAULT 0;
ALTER TABLE message_sources ADD COLUMN symbol TEXT NOT NULL DEFAULT '';

-- A chunk still in the index has not been re-read since the answer, so the
-- file's sha is the commit it was read at.
UPDATE message_sources
SET (repo, path, sha, start_line, end_line, symbol) = (
    SELECT f.repo, f.path, f.sha, c.start_line, c.end_line, c.symbol
    FROM chunks c JOIN files f ON f.id = c.file_id
    WHERE c.id = message_sources.chunk_id)
WHERE chunk_id <> 0
  AND EXISTS (SELECT 1 FROM chunks c WHERE c.id = message_sources.chunk_id);

UPDATE message_sources
SET (repo, sha) = (
    SELECT c.repo, c.sha FROM commits c WHERE c.id = message_sources.commit_id)
WHERE commit_id <> 0
  AND EXISTS (SELECT 1 FROM commits c WHERE c.id = message_sources.commit_id);
