-- kind becomes part. One word for one thing: the Projects page has always
-- headed this column "Part", and repos.yaml called it kind, so a reader
-- comparing the page against the file had to know they were the same field.
--
-- Renamed, not added beside: two columns would mean two answers to "what part
-- does this repository play", and the value is re-synced from repos.yaml on
-- every start anyway. `kind:` in the YAML is now an unknown key, which Load
-- refuses outright rather than reading as an empty part.
ALTER TABLE repo_state RENAME COLUMN kind TO part;
