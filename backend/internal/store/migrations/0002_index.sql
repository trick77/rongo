-- repo_state: one row per repository rongo knows about. Rows survive a repo
-- leaving repos.yaml (enabled = 0) rather than being deleted: a typo in the
-- YAML must not destroy hours of indexing. Only an explicit admin purge removes
-- the index.
CREATE TABLE repo_state (
    name        TEXT PRIMARY KEY,
    clone_url   TEXT NOT NULL,
    branch      TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_sha    TEXT NOT NULL DEFAULT '',
    last_run_at TEXT NOT NULL DEFAULT '',
    -- last_error is surfaced on the Repos page. A configured branch that
    -- vanished upstream lands here; it must never be a silent stop, because a
    -- frozen index looks healthy while answers come from months-old code.
    last_error  TEXT NOT NULL DEFAULT '',
    file_count  INTEGER NOT NULL DEFAULT 0,
    chunk_count INTEGER NOT NULL DEFAULT 0
);

-- files: one row per indexed path at the commit it was indexed from, so every
-- chunk is attributable to an exact repo/branch/file/sha.
--
-- skip_reason is set for a file that was deliberately NOT indexed (vendored,
-- generated, binary, too large, secret-bearing). The row still exists so the
-- answer layer can say "that file exists but was not indexed" instead of
-- pretending it is absent — the "never invent" invariant applied to the index.
CREATE TABLE files (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo        TEXT NOT NULL REFERENCES repo_state(name) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    sha         TEXT NOT NULL,
    lang        TEXT NOT NULL DEFAULT '',
    size        INTEGER NOT NULL DEFAULT 0,
    skip_reason TEXT NOT NULL DEFAULT '',
    UNIQUE (repo, path)
);

CREATE INDEX idx_files_repo ON files(repo);

-- symbols: from ctags. Not embedded — this is the exact-name lookup table and
-- the basis of the cross-repo reference walk.
CREATE TABLE symbols (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    kind    TEXT NOT NULL DEFAULT '',
    line    INTEGER NOT NULL DEFAULT 0,
    scope   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_symbols_name ON symbols(name);
CREATE INDEX idx_symbols_file ON symbols(file_id);

-- chunks: the unit of retrieval. text is the ENRICHED text that was embedded
-- (breadcrumb + enclosing symbols + doc comment + body); raw_text is the source
-- only, which is what the keyword lane indexes and what a citation quotes.
CREATE TABLE chunks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id      INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    ordinal      INTEGER NOT NULL,
    start_line   INTEGER NOT NULL,
    end_line     INTEGER NOT NULL,
    symbol       TEXT NOT NULL DEFAULT '',
    text         TEXT NOT NULL,
    raw_text     TEXT NOT NULL,
    token_count  INTEGER NOT NULL DEFAULT 0,
    -- content_hash keys the embedding cache. Moved or renamed code keeps its
    -- hash and is never re-embedded.
    content_hash TEXT NOT NULL,
    UNIQUE (file_id, ordinal)
);

CREATE INDEX idx_chunks_file ON chunks(file_id);
CREATE INDEX idx_chunks_hash ON chunks(content_hash);

-- chunks_vec: the semantic lane. rowid == chunks.id. vec0 cannot appear in
-- triggers or FK cascades, so this 1:1 bridging is maintained by hand, in the
-- same transaction as the chunks write.
CREATE VIRTUAL TABLE chunks_vec USING vec0(
    embedding float[1536]
);

-- chunks_fts: the keyword lane over the RAW text, keyed 1:1 by rowid ==
-- chunks.id. Standalone, so it is mirror-managed in the same transaction.
-- Hybrid search fuses this with the vec0 neighbours: PromoMailJob is found here
-- literally, "Teaser-Mail" is found by the vector lane semantically.
CREATE VIRTUAL TABLE chunks_fts USING fts5(raw_text);

-- embed_cache: content hash -> vector, so re-indexing unchanged content costs
-- nothing. In dev the whole corpus is re-indexed constantly; this is what keeps
-- that loop bearable, and it cuts the production diff-reindex bill too.
--
-- Keyed by (content_hash, model): the same hash under a different model is a
-- MISS, which is what makes the small-vs-large comparison honest instead of
-- silently reusing the first model's vectors.
CREATE TABLE embed_cache (
    content_hash TEXT NOT NULL,
    model        TEXT NOT NULL,
    dim          INTEGER NOT NULL,
    embedding    BLOB NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (content_hash, model)
);

-- repo_deps: from the dependency manifests. Not embedded. This is the hard
-- signal that separates composition from ambiguity: if candidate A depends on
-- candidate B they are parts of one mechanism, and rongo must answer about all
-- of them instead of asking which one is meant.
CREATE TABLE repo_deps (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo       TEXT NOT NULL REFERENCES repo_state(name) ON DELETE CASCADE,
    coordinate TEXT NOT NULL,
    direction  TEXT NOT NULL CHECK (direction IN ('publishes', 'requires')),
    UNIQUE (repo, coordinate, direction)
);

CREATE INDEX idx_repo_deps_coordinate ON repo_deps(coordinate);
