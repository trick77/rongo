-- Whether a card was answered is read off the messages: EXISTS (a row whose
-- from_clarification_id is the card's). Without an index that is a scan of
-- every message per card, on every thread read. Partial, because almost every
-- row is NULL; the predicate "= c.id" implies NOT NULL, so the index applies.
CREATE INDEX idx_messages_from_clarification ON messages(from_clarification_id)
	WHERE from_clarification_id IS NOT NULL;
