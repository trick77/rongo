-- message_usage: every paid call one turn made, one row each, with the
-- tokens the upstream reported. Tokens only: the price is configuration and
-- can change, so cost is computed when read, never stored. A turn that ended
-- early (asked back, found nothing, failed) still has rows for the calls it
-- made before it stopped; that is what makes a thread total honest.
CREATE TABLE message_usage (
    id                INTEGER PRIMARY KEY,
    message_id        INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    step              TEXT    NOT NULL,
    model             TEXT    NOT NULL,
    prompt_tokens     INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL
);
CREATE INDEX idx_message_usage_message ON message_usage(message_id);
