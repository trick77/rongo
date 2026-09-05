-- shared_threads: one thread, readable by anyone holding the link.
--
-- The token is stored RAW, unlike a session (sessions.token_hash keeps only a
-- SHA-256). A share link has to be shown again — the dialog reopens on it and
-- the Shared page lists it — so a hash would mean the owner could never see
-- their own link twice. It is a capability URL, not a password: 128 bits, never
-- indexed, and revocable in one click.
--
-- up_to_message_id is the ceiling. The public read returns only messages at or
-- below it, so a follow-up asked next week never appears on a link shared
-- today; raising it is the owner's own move.
--
-- Revoking is a flag, not a delete: the row and the token survive, so sharing
-- the same thread again hands back the same link rather than minting a second
-- one that has to be sent around all over again.
CREATE TABLE shared_threads (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    token            TEXT NOT NULL UNIQUE,
    -- One share per thread: a second link to the same conversation would be a
    -- second thing to revoke, and revoking one of them would look like a bug.
    thread_id        INTEGER NOT NULL UNIQUE REFERENCES threads(id) ON DELETE CASCADE,
    user_subject     TEXT NOT NULL REFERENCES users(subject) ON DELETE CASCADE,
    revoked          INTEGER NOT NULL DEFAULT 0 CHECK (revoked IN (0, 1)),
    up_to_message_id INTEGER NOT NULL,
    shared_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_shared_threads_user ON shared_threads(user_subject, shared_at);
