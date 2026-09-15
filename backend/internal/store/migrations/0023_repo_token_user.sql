-- token_user: the basic-auth username sent with a repository's token.
--
-- The poller rebuilds a repos.Spec from this table, not from the YAML, so a
-- field git needs at fetch time has to travel here like token_env does.
-- Empty means x-access-token, which is what every row was fetched with
-- before this column existed.
ALTER TABLE repo_state ADD COLUMN token_user TEXT NOT NULL DEFAULT '';
