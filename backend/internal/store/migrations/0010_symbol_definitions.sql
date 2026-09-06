-- symbols holds definitions only, from here on.
--
-- It was written from every ctags record, while internal/ask reads it as
-- "where is this name DEFINED" and follows what it finds. The two disagreed,
-- and the walk resolved struct fields and JSON keys: a question about routing
-- reached `type ladder struct` through its field `named`, and sixty-five array
-- elements of an eval fixture through the key `candidates`, while the
-- functions that do the routing never reached the prompt at all.
--
-- The writer now filters on indexer.isDefinition. That alone would leave the
-- existing rows in place for as long as their file's sha does not change,
-- which for a JSON fixture or a schema file is indefinitely — so the rows go
-- here rather than waiting for a re-index.
--
-- Deleting is safe in one direction only. Re-indexing a file rewrites its
-- symbols from scratch, but a file is re-indexed when its sha changes, so a
-- row deleted here comes back only if someone edits the file. Widening the Go
-- filter later therefore needs its own migration or a forced re-index, not
-- just the code change.
DELETE FROM symbols WHERE file_id IN (
    SELECT id FROM files WHERE lang IN ('json', 'yaml', 'xml')
);

DELETE FROM symbols WHERE kind IN ('member', 'package') AND file_id IN (
    SELECT id FROM files WHERE lang = 'go'
);

DELETE FROM symbols WHERE kind IN ('field', 'index') AND file_id IN (
    SELECT id FROM files WHERE lang = 'sql'
);
