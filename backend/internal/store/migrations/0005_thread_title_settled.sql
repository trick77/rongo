-- title_settled says the model's title call for this thread has finished, one
-- way or the other: it wrote a title, it came back empty and the placeholder
-- stands, or the reader typed a name of their own. Until it is set, the row's
-- title is only the question's first 48 runes — a label to tell the sidebar's
-- rows apart, never a title, and the header refuses to show a cut question in
-- place of one.
--
-- Existing rows are settled by definition: their title call ran when they were
-- asked, and a thread whose title never arrived must not read as pending for
-- the rest of its life.
ALTER TABLE threads ADD COLUMN title_settled INTEGER NOT NULL DEFAULT 0;
UPDATE threads SET title_settled = 1;
