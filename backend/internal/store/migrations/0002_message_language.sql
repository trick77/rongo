-- messages.language: the language the answer is written in. Stored per
-- message like audience, because the selector sits on the input field. A
-- thread nevertheless holds one language throughout: threads.AddQuestion pins
-- every later turn to the first turn's value, so the column repeats it rather
-- than mixing an English answer with a German one.
-- Validated in the handler against the allowlist in package ask; a value the
-- handler does not know falls back to 'en' there, never to an error.
ALTER TABLE messages ADD COLUMN language TEXT NOT NULL DEFAULT 'en';
