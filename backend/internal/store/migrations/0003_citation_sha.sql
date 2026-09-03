-- citations.sha: the commit the cited file was indexed at. The source viewer
-- reads the file at this commit, so a citation keeps pointing at the lines the
-- answer was written from after the branch has moved on. Empty for citations
-- recorded before this column existed; the viewer then falls back to the
-- commit the file is currently indexed at.
ALTER TABLE citations ADD COLUMN sha TEXT NOT NULL DEFAULT '';
