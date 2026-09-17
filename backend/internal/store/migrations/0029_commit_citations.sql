-- A commit citation: kind says which viewer opens it, subject and
-- committed_at are what the chip shows without a lookup, because a shared
-- page reads citations with no other table in reach. A file citation
-- leaves all three empty, so every row from before this decodes as one.
ALTER TABLE citations ADD COLUMN kind TEXT NOT NULL DEFAULT '';
ALTER TABLE citations ADD COLUMN subject TEXT NOT NULL DEFAULT '';
ALTER TABLE citations ADD COLUMN committed_at TEXT NOT NULL DEFAULT '';

-- A changes turn is written from commits, and its record has to say which,
-- the way chunk_id says which chunks: a re-explain answers from the same
-- evidence or refuses. One of the two ids is set per row, the other is 0.
-- Rebuilt rather than altered because the uniqueness was (message, chunk),
-- and every commit row would collide on chunk 0.
CREATE TABLE message_sources_new (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    chunk_id   INTEGER NOT NULL DEFAULT 0,
    commit_id  INTEGER NOT NULL DEFAULT 0,
    reason     TEXT NOT NULL,
    hop        INTEGER NOT NULL DEFAULT 0,
    UNIQUE (message_id, chunk_id, commit_id)
);
INSERT INTO message_sources_new (message_id, chunk_id, reason, hop)
    SELECT message_id, chunk_id, reason, hop FROM message_sources;
DROP TABLE message_sources;
ALTER TABLE message_sources_new RENAME TO message_sources;
CREATE INDEX idx_message_sources_message ON message_sources(message_id);
