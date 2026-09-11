-- message_usage: three figures the endpoint has always reported and the
-- decoder always threw away, plus how long the call took.
--
-- cached_tokens is prompt_tokens_details.cached_tokens: the part of the
-- prompt the upstream served from its cache. A SUBSET of prompt_tokens, not
-- an addition, and priced at cache_read — two orders of magnitude under the
-- input price on both MiMo deployments. Without it a thread's later turns,
-- which repeat the prefix of the first by construction, were costed as if
-- nothing had been cached.
--
-- reasoning_tokens is completion_tokens_details.reasoning_tokens: the part
-- of the completion spent thinking rather than writing. Also a subset.
--
-- ms is wall clock for the one call, request to last byte.
--
-- All three are NULLABLE on purpose, and rows written before this migration
-- keep NULL. A turn from then must not read as a call that cached nothing,
-- reasoned about nothing and took no time — the same rule the price table
-- has always kept, where absent and zero say different things.
ALTER TABLE message_usage ADD COLUMN cached_tokens    INTEGER;
ALTER TABLE message_usage ADD COLUMN reasoning_tokens INTEGER;
ALTER TABLE message_usage ADD COLUMN ms               INTEGER;
