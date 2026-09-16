-- starred is the reader's own mark on a thread: the rail lists starred
-- threads in their own section above the recent ones, however old they are.
-- A flag, never an ordering — threads keep listing by id — so starring
-- reorders nothing and touches no timestamp. The index serves the rail's
-- starred-only list, which otherwise scans every thread of the user.
ALTER TABLE threads ADD COLUMN starred INTEGER NOT NULL DEFAULT 0 CHECK (starred IN (0, 1));
CREATE INDEX idx_threads_user_starred ON threads(user_subject, starred, id DESC);
