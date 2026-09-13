-- repo_stages: the deployment stages an infrastructure repository declares in
-- repos.yaml — a name a reader can ask for, the directory its files live
-- under, and the other words for it. Written from repos.yaml by SyncSpecs and
-- replaced whole per repository, like repo_uses: declared, never inferred
-- from the tree, and a stage that leaves the file leaves the table.
CREATE TABLE repo_stages (
    repo    TEXT NOT NULL REFERENCES repo_state(name) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    -- prefix is the repo-relative directory with its trailing slash, "prod/".
    -- A path under it belongs to the stage; the search narrows on it with
    -- substr, never LIKE, so "_" in a directory name is not a wildcard.
    prefix  TEXT NOT NULL,
    -- aliases are the other whole words that name the stage, lower-cased and
    -- joined by a space; empty when there are none.
    aliases TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, name)
);

-- One forced re-index, for 0015's reason and one more. Two things this
-- release changes are written by the file pipeline, which an incremental poll
-- only runs over the paths a commit changed: property tokens, which an
-- untouched properties file would never get, and REDACTION, which matters
-- more — a configuration file indexed before it carried its credential values
-- in chunks, in the FTS mirror and in a vector, and only re-indexing the file
-- replaces those rows. Emptying last_sha makes the next poll index every
-- repository whole; unchanged chunks are re-read, never re-embedded.
UPDATE repo_state SET last_sha = '';
