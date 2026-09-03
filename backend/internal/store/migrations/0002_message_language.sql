-- messages.language: the language the answer is written in. Stored per
-- message like audience, because the selector sits on the input field and is
-- set per question - one thread may hold an English answer and a German one.
-- Validated in the handler against the allowlist in package ask; a value the
-- handler does not know falls back to 'en' there, never to an error.
ALTER TABLE messages ADD COLUMN language TEXT NOT NULL DEFAULT 'en';
