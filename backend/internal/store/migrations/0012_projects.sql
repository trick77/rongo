-- A repository belongs to a project: the product a reader is asked to choose
-- between. A repository that stands alone is a project of one named after
-- itself, so the column is never meaningfully empty once repos.yaml has been
-- loaded — repos.Load refuses an entry without it.
--
-- kind and description say what part a repository plays. Both are written by
-- hand in repos.yaml and both reach the answer prompt: with two backends in one
-- project, "backend" alone does not say which one the storefront talks to, and
-- "Kafka consumer, ingests order events" is what separates a queue reader from
-- a second HTTP API. Neither is ever embedded, indexed or cited.
ALTER TABLE repo_state ADD COLUMN project     TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_state ADD COLUMN kind        TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_state ADD COLUMN description TEXT NOT NULL DEFAULT '';

-- repo_uses is the hand-declared edge repo_deps cannot see: a Go backend and a
-- TypeScript UI share no go.mod, so nothing in a manifest says the UI calls the
-- API. Shaped like repo_deps deliberately — cascading off repo_state, replaced
-- wholesale per repository — so a repository leaving repos.yaml drops its edges
-- with its row.
--
-- Written from repos.yaml by SyncSpecs, NOT from a checkout by the indexer:
-- unlike go.mod there is no file in the code to parse, and inferring the edge
-- would be a model's opinion about two repository names.
--
-- No FK on `uses`: repos.Load already refuses an entry naming a repository that
-- does not exist or sits in another project, and a constraint here would make
-- the order rows are inserted in matter.
CREATE TABLE repo_uses (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    repo TEXT NOT NULL REFERENCES repo_state(name) ON DELETE CASCADE,
    uses TEXT NOT NULL,
    UNIQUE (repo, uses)
);
