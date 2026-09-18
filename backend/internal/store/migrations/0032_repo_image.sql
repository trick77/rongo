-- image: the container image a repository is built into, tag-less, as its
-- repos.yaml entry declares it. Structure like part and description: synced
-- from the file on every start, never read from the checkout, never cited.
-- It is what a release turn pairs a version found in the infrastructure
-- repository's overlays with the repository whose tag it is.
ALTER TABLE repo_state ADD COLUMN image TEXT NOT NULL DEFAULT '';
