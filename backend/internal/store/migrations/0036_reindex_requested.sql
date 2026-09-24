-- reindex_requested is a request generation, not a flag: an admin asked for a
-- full re-index of this repository, and the poller honours it at the start of
-- its next pass. Bumped by every request and cleared only when the run that
-- read a given generation finishes, so a request that arrives while a run is
-- already under way is not cleared by that run's end.
ALTER TABLE repo_state ADD COLUMN reindex_requested INTEGER NOT NULL DEFAULT 0;
