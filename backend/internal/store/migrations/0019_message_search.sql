-- message_fts: full-text search over the questions and answers a thread
-- holds, for the Threads page — "which thread was that in?" answered by what
-- was said in it, not only by its title.
--
-- Not a breach of the rule against storing model-written text about code:
-- that rule guards the retrieval index, where model prose written at index
-- time goes stale and pulls a vector towards a claim no code honours. This
-- is a derived index over answers the record already stores, read only by a
-- reader searching their own threads, shown only as a snippet under a row
-- they asked the question for, never retrieved for or cited in an answer.
--
-- A standalone FTS5 table (not external-content), ../loom's shape: thread_id
-- rides along UNINDEXED so a hit can be grouped per thread and joined to its
-- owner, and only question and answer are indexed. unicode61 rather than
-- porter: answers come in German, French and Italian as well as English,
-- and English stemming would mangle three of the four. remove_diacritics 2
-- so "Strasse" and "Straße", "resume" and "résumé" find each other.
--
-- The FTS rowid is pinned to messages.rowid, so the delete and update
-- triggers seek by the FTS primary key instead of scanning an UNINDEXED
-- column — which matters when a thread delete cascades into every message.
CREATE VIRTUAL TABLE message_fts USING fts5(
    thread_id UNINDEXED,
    question,
    answer,
    tokenize = 'unicode61 remove_diacritics 2'
);

-- Backfill what is already on record.
INSERT INTO message_fts (rowid, thread_id, question, answer)
SELECT rowid, thread_id, question, answer FROM messages;

-- Kept in lockstep with messages by triggers, whichever code path writes:
-- the question is inserted first, the answer lands later when the stream
-- finishes, and a thread delete cascades through here.
CREATE TRIGGER message_fts_ai AFTER INSERT ON messages BEGIN
    INSERT INTO message_fts (rowid, thread_id, question, answer)
    VALUES (new.rowid, new.thread_id, new.question, new.answer);
END;

CREATE TRIGGER message_fts_ad AFTER DELETE ON messages BEGIN
    DELETE FROM message_fts WHERE rowid = old.rowid;
END;

CREATE TRIGGER message_fts_au AFTER UPDATE OF question, answer ON messages BEGIN
    DELETE FROM message_fts WHERE rowid = old.rowid;
    INSERT INTO message_fts (rowid, thread_id, question, answer)
    VALUES (new.rowid, new.thread_id, new.question, new.answer);
END;
