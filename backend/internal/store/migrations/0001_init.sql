-- users: one row per identity rongo has seen. subject is the stable id from
-- the auth mode in use (the fixed dev user, or the OIDC subject later).
CREATE TABLE users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    subject    TEXT NOT NULL UNIQUE,
    email      TEXT NOT NULL DEFAULT '',
    is_admin   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- sessions: server-side opaque tokens. Only the SHA-256 of the token is
-- stored, so a database copy cannot be replayed as a login.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);

CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- repo_state: one row per repository rongo knows about. Rows survive a repo
-- leaving repos.yaml (enabled = 0) rather than being deleted: a typo in the
-- YAML must not destroy hours of indexing. Only an explicit admin purge removes
-- the index.
CREATE TABLE repo_state (
    name        TEXT PRIMARY KEY,
    clone_url   TEXT NOT NULL,
    branch      TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    -- token_env is the NAME of the environment variable holding this
    -- repository's forge token, copied from repos.yaml. The value is never
    -- stored here, or anywhere else on disk. Without this column the poller has
    -- no way to know which variable to read, and every private repository is
    -- fetched anonymously.
    token_env   TEXT NOT NULL DEFAULT '',
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
--
-- The dimension is substituted by store.Migrate from BACKEND_EMBED_DIM rather
-- than hardcoded: text-embedding-3-small is 1536 and -large is 3072, and the
-- two are compared by measurement. store.BuiltDim reads the dimension back out
-- of this DDL, so a database built for one model cannot be used quietly with
-- another.
CREATE VIRTUAL TABLE chunks_vec USING vec0(
    embedding float[{{EMBED_DIM}}]
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

-- threads: one conversation. The thread is a RECORD, not a working document:
-- a follow-up adds a message, it never rewrites one, and a corrected
-- clarification starts a new turn. Nothing in the schema lets an answer be
-- edited in place, which is deliberate — someone reading the thread later has
-- to be able to see what was actually said at the time.
CREATE TABLE threads (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_subject TEXT NOT NULL REFERENCES users(subject) ON DELETE CASCADE,
    -- title starts as the first words of the question and is replaced once the
    -- non-Pro title call answers. It is never empty: the sidebar entry has to
    -- appear the moment the question is sent, and the title must never make the
    -- answer wait.
    title        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_threads_user ON threads(user_subject, created_at);

-- messages: one question and its answer. audience is stored per message rather
-- than per thread because the role switch sits on the input field and is set
-- per message — the same thread legitimately holds a BA answer and a DEV one.
CREATE TABLE messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id  INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    ordinal    INTEGER NOT NULL,
    audience   TEXT NOT NULL CHECK (audience IN ('ba', 'dev')),
    question   TEXT NOT NULL,
    -- answer is written once the stream completes. An empty answer with a
    -- non-empty error is a turn that failed, and it stays in the record: a
    -- disappearing question would leave the reader wondering what they asked.
    answer     TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    -- Where this turn came from, when it resumed a clarification. Recorded on
    -- the NEW message rather than as a `chosen` flag on the clarification: the
    -- answer is what closes the card, so a turn that never produced one leaves
    -- it open for a retry, and nothing in the record is ever overwritten.
    from_clarification_id INTEGER REFERENCES clarifications(id) ON DELETE SET NULL,
    from_candidate_idx    INTEGER NOT NULL DEFAULT -1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (thread_id, ordinal)
);

CREATE INDEX idx_messages_thread ON messages(thread_id, ordinal);

-- citations: what a marker in the answer points at. Repo, branch, file and
-- line, because a forge URL without the branch may 404 off the default branch.
--
-- Stored per message rather than derived at read time: the answer cites the
-- code as it was gathered, and re-deriving it after a re-index would silently
-- move a citation onto different lines.
CREATE TABLE citations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    marker     INTEGER NOT NULL,
    repo       TEXT NOT NULL,
    branch     TEXT NOT NULL,
    path       TEXT NOT NULL,
    start_line INTEGER NOT NULL DEFAULT 0,
    end_line   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (message_id, marker)
);

CREATE INDEX idx_citations_message ON citations(message_id);

-- clarifications: a turn that ended by asking which mechanism was meant.
--
-- The understanding is stored with it because the resumed turn must not
-- re-derive its own search terms: a second understanding call can produce
-- different ones, and the answer would then be built from material the card
-- never offered.
CREATE TABLE clarifications (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id    INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    understanding TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (message_id)
);

-- clarification_candidates: what the card offered, in the order it offered it.
--
-- hits is the fused hit list this candidate was built from, as JSON. Storing
-- only the module name would force the resumed turn to search again, and a
-- second search can rank differently — the reader would pick one candidate and
-- get an answer gathered from another.
CREATE TABLE clarification_candidates (
    clarification_id INTEGER NOT NULL REFERENCES clarifications(id) ON DELETE CASCADE,
    idx              INTEGER NOT NULL,
    repo             TEXT NOT NULL,
    branch           TEXT NOT NULL,
    module_key       TEXT NOT NULL,
    title            TEXT NOT NULL,
    summary          TEXT NOT NULL,
    hits             TEXT NOT NULL,
    UNIQUE (clarification_id, idx)
);

-- message_sources: what the answer was actually written from, as chunk ids.
--
-- Ids, not copies: re-explaining reads the chunks back. If a re-index removed
-- one, rongo says the basis is gone rather than answering the same question
-- from different code.
CREATE TABLE message_sources (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    chunk_id   INTEGER NOT NULL,
    reason     TEXT NOT NULL,
    hop        INTEGER NOT NULL DEFAULT 0,
    UNIQUE (message_id, chunk_id)
);

CREATE INDEX idx_message_sources_message ON message_sources(message_id);
