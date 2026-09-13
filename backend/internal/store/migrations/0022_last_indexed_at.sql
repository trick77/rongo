-- last_indexed_at: when the index was last written for this repository.
--
-- last_run_at moves on every poll outcome (indexed, checked and unchanged,
-- failed), so it answers "is the poller alive", never "is my push in the
-- answers yet". The Repos page read it as the latter. This column moves only
-- when MarkIndexed runs. Backfilled from last_run_at where an index exists:
-- the best fact on record, corrected by the next index run.
ALTER TABLE repo_state ADD COLUMN last_indexed_at TEXT NOT NULL DEFAULT '';
UPDATE repo_state SET last_indexed_at = last_run_at WHERE last_sha <> '';
