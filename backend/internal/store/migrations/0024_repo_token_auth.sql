-- token_auth: how a repository's token travels, '' for basic auth or
-- 'bearer' for an Authorization header. Same reason as token_user: the
-- poller rebuilds its Spec from this table, and a fetch-time setting that
-- did not travel would silently fall back to basic auth.
ALTER TABLE repo_state ADD COLUMN token_auth TEXT NOT NULL DEFAULT '';
