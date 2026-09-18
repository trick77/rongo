-- A library is a repository several products are built on, declared once in
-- the top-level `libraries:` block of repos.yaml and owned by no project. It
-- keeps `project` set to its own name, so every reader that groups by that
-- column sees a project of one and needs no second case; this flag is what
-- tells a uses edge into it apart from a cross-project edge, which
-- projects.loadUses drops on sight. Written from repos.yaml by SyncSpecs like
-- project and part, and like them never embedded, indexed or cited.
ALTER TABLE repo_state ADD COLUMN library INTEGER NOT NULL DEFAULT 0;
