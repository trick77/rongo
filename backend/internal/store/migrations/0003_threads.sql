-- threads: one conversation. The thread is a RECORD, not a working document:
-- a follow-up adds a message, it never rewrites one, and a corrected
-- clarification starts a new turn. Nothing in the schema lets an answer be
-- edited in place, which is deliberate — someone reading the thread later has
-- to be able to see what was actually said at the time.
CREATE TABLE threads (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_subject TEXT NOT NULL REFERENCES users(subject) ON DELETE CASCADE,
    -- title starts as the first words of the question and is replaced once the
    -- non-Pro title call answers. It is never empty: the sidebar entry has to
    -- appear the moment the question is sent, and the title must never make the
    -- answer wait.
    title        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_threads_user ON threads(user_subject, created_at);

-- messages: one question and its answer. audience is stored per message rather
-- than per thread because the role switch sits on the input field and is set
-- per message — the same thread legitimately holds a BA answer and a DEV one.
CREATE TABLE messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id  INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    ordinal    INTEGER NOT NULL,
    audience   TEXT NOT NULL CHECK (audience IN ('ba', 'dev')),
    question   TEXT NOT NULL,
    -- answer is written once the stream completes. An empty answer with a
    -- non-empty error is a turn that failed, and it stays in the record: a
    -- disappearing question would leave the reader wondering what they asked.
    answer     TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (thread_id, ordinal)
);

CREATE INDEX idx_messages_thread ON messages(thread_id, ordinal);

-- citations: what a marker in the answer points at. Repo, branch, file and
-- line, because a forge URL without the branch may 404 off the default branch.
--
-- Stored per message rather than derived at read time: the answer cites the
-- code as it was gathered, and re-deriving it after a re-index would silently
-- move a citation onto different lines.
CREATE TABLE citations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    marker     INTEGER NOT NULL,
    repo       TEXT NOT NULL,
    branch     TEXT NOT NULL,
    path       TEXT NOT NULL,
    start_line INTEGER NOT NULL DEFAULT 0,
    end_line   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (message_id, marker)
);

CREATE INDEX idx_citations_message ON citations(message_id);
