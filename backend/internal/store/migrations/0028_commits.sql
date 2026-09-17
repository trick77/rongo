-- commits: the first-parent history of every indexed branch, one row per
-- commit, written by the poller beside the file index. It answers "what
-- changed since" from the record rather than from whichever document
-- happens to narrate a change: the file index has no date on anything, and
-- a question about the last two days would otherwise land on a measurement
-- write-up from a month ago.
--
-- author is stored and never served. A shared page is public, and a name
-- on it is a different decision from a date.
CREATE TABLE commits (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo         TEXT NOT NULL REFERENCES repo_state(name) ON DELETE CASCADE,
    sha          TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    author       TEXT NOT NULL DEFAULT '',
    subject      TEXT NOT NULL,
    body         TEXT NOT NULL DEFAULT '',
    paths        TEXT NOT NULL DEFAULT '',
    UNIQUE (repo, sha)
);
CREATE INDEX idx_commits_repo_date ON commits(repo, committed_at DESC);

-- commits_fts: the keyword lane over subject, body and paths, keyed 1:1 by
-- rowid == commits.id. Standalone like chunks_fts, so it is mirror-managed
-- in the same transaction and purged by hand (an fts5 table takes part in
-- no cascade).
CREATE VIRTUAL TABLE commits_fts USING fts5(subject, body, paths);

-- One forced re-index, for 0015's reason: an incremental poll logs only the
-- commits since last_sha, so on an existing deployment the history would
-- start at the next push. Nothing unchanged is re-embedded.
UPDATE repo_state SET last_sha = '';
