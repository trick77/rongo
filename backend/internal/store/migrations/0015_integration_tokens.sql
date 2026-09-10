-- integration_tokens: the named destinations a file talks about — a queue or
-- topic name, an HTTP route. They are the only thing linking a producer to its
-- consumer across repository boundaries, because such a pair shares no import,
-- no type and no symbol: only the string "shipping-task" or "/paymentAuth".
--
-- Extracted, never inferred and never generated: a token is a string literal
-- that stood next to a messaging call, a route annotation or a router
-- registration. No model runs at index time, so a token cannot be a
-- hallucination and cannot go stale independently of the file it came from.
--
-- Rows hang off files(id) with ON DELETE CASCADE, exactly like symbols, so a
-- deleted or re-indexed file takes its tokens with it.
CREATE TABLE integration_tokens (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    -- kind is 'route' or 'destination'. Kept apart because they are matched
    -- apart: "/orders" as a route must never be joined to a queue called
    -- "/orders", however unlikely that is.
    kind    TEXT NOT NULL,
    value   TEXT NOT NULL,
    line    INTEGER NOT NULL DEFAULT 0
);

-- The value index is what an edge lookup uses: given a file's tokens, find
-- every other file carrying the same one.
CREATE INDEX idx_integration_tokens_value ON integration_tokens(kind, value);
CREATE INDEX idx_integration_tokens_file ON integration_tokens(file_id);

-- One forced re-index, for the same reason 0010 deletes rows here rather than
-- waiting: an incremental poll passes only the paths a commit CHANGED
-- (indexer/poller.go, "only the changed paths"), and tokens are written by the
-- file pipeline. Without this, an existing database would carry an empty
-- integration_tokens for as long as its files sit untouched, which for a
-- settled service is indefinitely, and every lookup would return nothing while
-- looking perfectly healthy.
--
-- Emptying last_sha makes the next poll pass nil paths, which indexes the
-- repository whole. It is not expensive: the embedding cache is keyed on
-- content hash and repository-independent, so unchanged chunks are re-read and
-- re-chunked but never re-embedded.
UPDATE repo_state SET last_sha = '';
