-- pasted_texts is which trailing blocks of question the reader pasted rather
-- than typed: [{"text":"panic: boom\n...","lines":12}] as JSON, '[]' on every
-- turn that carried none and on every turn written before this column existed.
--
-- Render-only. The paste is already inside question, where every prompt, the
-- search and the title read it; this column only tells the page which part to
-- fold into a chip instead of setting it as the reader's own prose. Nothing in
-- the pipeline reads it.
ALTER TABLE messages ADD COLUMN pasted_texts TEXT NOT NULL DEFAULT '[]';
