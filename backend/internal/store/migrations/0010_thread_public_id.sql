-- public_id is the thread's address. It replaces the row number in every URL
-- and on every API path: /thread/19 said how many threads exist on the box and
-- made every one of them reachable by typing.
--
-- It is minted the same way a share token is — 16 bytes of crypto/rand as 22
-- URL-safe characters — and it is NOT one. A share token is the whole
-- authorisation for a public page and can be revoked; this is an address behind
-- the session cookie, and every handler that takes it still checks ownership.
-- Two separate values on purpose: revoking a share must not change the URL its
-- owner has in a bookmark, and handing someone the owner URL must not hand them
-- the share.
--
-- The column is filled by BackfillPublicIDs in Go, not here: SQLite has no
-- base64, so SQL could only mint hex, and an id that does not look like the
-- share token is the one thing this change exists to avoid. The default is
-- therefore empty for a moment on an existing database, and the unique index is
-- partial so those rows do not collide with each other before the backfill runs.
ALTER TABLE threads ADD COLUMN public_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_threads_public_id ON threads(public_id) WHERE public_id != '';
