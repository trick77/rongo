-- memories: the standing instructions one reader gave in chat, kept across
-- threads and injected into every answer. Keyed on the user, never the
-- thread: deleting a conversation must not forget a rule. text is English
-- whatever language the reader wrote in, one sentence, so one row serves
-- threads in four languages. scope is a project or repository name the rule
-- is limited to, empty for a rule that holds everywhere. kind is 'stated' for
-- every row written today; the column is there so an inferred layer, should
-- one ever be measured worth it, does not need a second table.
CREATE TABLE memories (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_subject      TEXT NOT NULL REFERENCES users(subject) ON DELETE CASCADE,
    text              TEXT NOT NULL,
    kind              TEXT NOT NULL DEFAULT 'stated' CHECK (kind IN ('stated', 'inferred')),
    scope             TEXT NOT NULL DEFAULT '',
    -- The turn the rule was said in, for the "from thread" link on the
    -- Memory page. NULL once that message is gone; the rule stays.
    source_message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
    created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_memories_user ON memories(user_subject, created_at);

-- The rule a turn saved, so the "Remembered" chip and its undo survive a
-- reload. NULL on every other turn, and again once the rule is deleted: the
-- read joins the row and finds nothing, which is exactly what "forgotten"
-- looks like in the record.
ALTER TABLE messages ADD COLUMN memory_id INTEGER REFERENCES memories(id) ON DELETE SET NULL;
