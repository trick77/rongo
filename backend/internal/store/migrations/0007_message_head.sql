-- head_message_id says which turn a row belongs to. 0 means the row IS the
-- head: it carried a question the reader typed.
--
-- A row is one answer ATTEMPT, not one question. Resuming a clarification,
-- retrying a failure and re-explaining for the other audience each append a
-- row that copies the question text of the row it continues, because the
-- record is append-only and nothing here is ever overwritten. Without this
-- column the copy is all there is, so the record asserts the same question was
-- asked three times and the page has no honest way to say otherwise. The link
-- is what lets one question be printed once.
--
-- It points at the HEAD of the turn, never at the immediate predecessor: a
-- reader can pick, fail, pick again, and a chain would have to be walked at
-- read time to find the question. Written when the row is inserted, not when
-- the answer lands, so a turn that failed is still grouped under the question
-- it failed at. That is the difference from from_clarification_id, which is
-- written at answer time because it closes the card, and which stays.
ALTER TABLE messages ADD COLUMN head_message_id INTEGER NOT NULL DEFAULT 0;

-- Backfill, exactly and only where the record already knows the answer: a
-- resumed turn names the clarification it came from, and the clarification
-- names the message it was asked on. No guessing from matching question text.
-- Rows retried or re-explained before this column existed keep head 0 and go
-- on rendering as their own turns; there is nothing stored that separates
-- them from a reader who typed the same question twice, and inventing a
-- heuristic here would regroup the record on a hunch.
--
-- The walk is recursive because a resumed turn can itself end in a card: the
-- head is the root of the chain, not the row one step up.
WITH RECURSIVE
    resumed(id, parent) AS (
        SELECT m.id, c.message_id
        FROM messages m
        JOIN clarifications c ON c.id = m.from_clarification_id
    ),
    walk(id, at) AS (
        SELECT id, parent FROM resumed
        UNION ALL
        SELECT w.id, r.parent FROM walk w JOIN resumed r ON r.id = w.at
    )
UPDATE messages SET head_message_id = (
    SELECT w.at FROM walk w
    WHERE w.id = messages.id AND w.at NOT IN (SELECT id FROM resumed)
)
WHERE id IN (SELECT id FROM resumed);
