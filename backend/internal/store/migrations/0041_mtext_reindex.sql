-- An M/TEXT include names a file by its stem: the file name without the "#"
-- a text block's name starts with and without the extension. The stem is
-- stored case folded (Unicode, as Windows compares names) for M/TEXT files
-- and '' for every other file, so resolving an include is an equality the
-- index serves, per repository: the template's own one first, then a
-- library. Kept out of symbols on purpose: a stem there was a definition the
-- name walk followed corpus-wide.
ALTER TABLE files ADD COLUMN stem TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_files_stem ON files (stem, repo) WHERE stem <> '';

-- One forced re-index, for 0021's reason: .mtxm and .mtxt became a language
-- of their own (files.lang = 'mtext', chunked by head, top-level block and
-- preview, the stem above set), and their includes are new integration
-- tokens. .translation is skipped as data from now on. An incremental poll
-- only touches paths that changed since last_sha, so every existing template
-- would keep its line windows, no stem and no include until somebody edited
-- it.
UPDATE repo_state SET last_sha = '';
