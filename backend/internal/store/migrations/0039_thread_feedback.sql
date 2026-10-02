-- thread_feedback is the reader's verdict on a thread: was it helpful, and if
-- not, optionally why. One row per thread, not per answer — the reader rates
-- the conversation, and changing their mind replaces the row.
--
-- up_to_message_id is the newest finished answer when the verdict was given.
-- The thread keeps growing after that, and the verdict must say which turns
-- it actually covered instead of silently stretching over later ones.
--
-- Stored, never read back into anything: no prompt, no memory block, no
-- answer, no shared page. The owner is the thread's.
CREATE TABLE thread_feedback (
    thread_id        INTEGER PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
    verdict          INTEGER NOT NULL CHECK (verdict IN (-1, 1)),
    reason           TEXT NOT NULL DEFAULT ''
                     CHECK (reason IN ('', 'wrong', 'incomplete', 'missed_code', 'too_long', 'wrong_repo')),
    up_to_message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    updated_at       TEXT NOT NULL DEFAULT (datetime('now'))
);
