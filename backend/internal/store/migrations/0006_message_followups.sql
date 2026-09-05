-- followups is what the reader was offered to ask next once this turn was
-- answered: ["What happens on a re-index?", ...] as JSON, or '' on every turn
-- that got none - a card, a failure, a nothing-found.
--
-- Stored rather than regenerated on read: the pills are part of the record the
-- reader saw, and re-asking a model at read time would cost a call per thread
-- load and offer different questions every time. This is text a person reads,
-- like threads.title and a candidate's title, not a model's summary of code.
ALTER TABLE messages ADD COLUMN followups TEXT NOT NULL DEFAULT '';
