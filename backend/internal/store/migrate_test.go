package store

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"
)

// openTemp gives each test its own database file.
func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("Open() err = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrate_createsSchemaAndIsIdempotent(t *testing.T) {
	// Given
	db := openTemp(t)

	// When
	if err := Migrate(db, 1536); err != nil {
		t.Fatalf("first Migrate() err = %v", err)
	}
	if err := Migrate(db, 1536); err != nil {
		t.Fatalf("second Migrate() err = %v", err)
	}

	// Then: the tables exist ...
	for _, table := range []string{"users", "sessions", "schema_migrations"} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}

	// ... and the second run recorded nothing extra. Asserted as "unchanged by
	// the second run" rather than against a fixed number, so adding a migration
	// does not require editing this test — the property under test is
	// idempotency, not the migration count.
	var afterSecond int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&afterSecond); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if err := Migrate(db, 1536); err != nil {
		t.Fatalf("third Migrate() err = %v", err)
	}
	var afterThird int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&afterThird); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if afterThird != afterSecond {
		t.Errorf("schema_migrations rows = %d after a third run, want %d — Migrate is not idempotent",
			afterThird, afterSecond)
	}
	if afterSecond == 0 {
		t.Error("no migrations recorded at all; the runner did not apply anything")
	}
}

func TestMigrateBuildsTheWholeSchemaFromOneFile(t *testing.T) {
	db := openTemp(t)
	if err := Migrate(db, 1536); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Every embedded file is recorded, no more and no fewer: a migration the
	// runner silently skipped would leave production one column short while
	// every test database, built fresh, has it.
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	var files int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&files); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if files != len(entries) {
		t.Errorf("schema_migrations has %d rows, want %d — one per embedded file", files, len(entries))
	}

	for _, table := range []string{
		"users", "sessions", "repo_state", "files", "symbols", "chunks",
		"embed_cache", "repo_deps", "threads", "messages", "citations",
		"clarifications", "clarification_candidates", "message_sources",
	} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing after migrate: %v", table, err)
		}
	}

	// The two columns that carry a resumed turn back to the card it came from,
	// the language column 0002 adds on top of the squashed init, and the head
	// link 0007 adds to say which turn a row is an attempt at.
	for _, col := range []string{"from_clarification_id", "from_candidate_idx", "language", "head_message_id"} {
		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM pragma_table_info('messages') WHERE name=?`, col).Scan(&n); err != nil {
			t.Fatalf("pragma messages: %v", err)
		}
		if n != 1 {
			t.Errorf("messages.%s missing", col)
		}
	}
}

// TestMigrate_0024RebuildsTheKeywordLaneWithAnAuxColumn walks 0024 over a
// database that already holds indexed content, which is the only state it has
// to be right in. A fresh database applies it to an empty chunks table and
// proves nothing about the backfill or about the forced re-index.
//
// The pre-0024 state is rebuilt by hand rather than by pinning the runner to a
// version: Migrate has no "up to here", and a test that could stop halfway
// would be a second, untested code path in the runner.
func TestMigrate_0024RebuildsTheKeywordLaneWithAnAuxColumn(t *testing.T) {
	// Given: the schema as it stood before 0024 — one FTS column, a chunk in
	// it, and a repository parked at an indexed commit.
	db := openTemp(t)
	if err := Migrate(db, 1536); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE version = '0024_fts_aux.sql'`,
		`DROP TABLE chunks_fts`,
		`CREATE VIRTUAL TABLE chunks_fts USING fts5(raw_text)`,
		`INSERT INTO repo_state (name, clone_url, last_sha) VALUES ('shop', 'file:///shop', 'deadbeef')`,
		`INSERT INTO files (repo, path, sha, lang) VALUES ('shop', 'src/A.java', 'deadbeef', 'java')`,
		`INSERT INTO chunks (file_id, ordinal, start_line, end_line, symbol, text, raw_text, token_count, content_hash)
			VALUES (1, 0, 1, 3, 'run', 'enriched', 'void run() { sender.send(); }', 5, 'h1')`,
		`INSERT INTO chunks_fts (rowid, raw_text) SELECT id, raw_text FROM chunks`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("rebuild the pre-0024 state (%s): %v", stmt, err)
		}
	}

	// When
	if err := Migrate(db, 1536); err != nil {
		t.Fatalf("migrate 0024: %v", err)
	}

	// Then: the second column exists ...
	var cols int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('chunks_fts') WHERE name = 'aux'`).Scan(&cols); err != nil {
		t.Fatalf("pragma chunks_fts: %v", err)
	}
	if cols != 1 {
		t.Error("chunks_fts has no aux column after 0024")
	}

	// ... the row survived the rebuild, so the keyword lane is not silently
	// empty between the migration and the next poll ...
	var found int
	if err := db.QueryRow(`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH 'sender'`).Scan(&found); err != nil {
		t.Fatalf("match the rebuilt row: %v", err)
	}
	if found != 1 {
		t.Errorf("the rebuilt lane matches %d rows, want the backfilled chunk", found)
	}

	// ... and every repository is back at "nothing indexed yet", because aux
	// cannot be derived in SQL and only a full run can write it.
	var sha string
	if err := db.QueryRow(`SELECT last_sha FROM repo_state WHERE name = 'shop'`).Scan(&sha); err != nil {
		t.Fatalf("read last_sha: %v", err)
	}
	if sha != "" {
		t.Errorf("last_sha = %q, want it cleared — an incremental poll never re-chunks an unchanged file", sha)
	}
}

func TestOpen_enablesWALAndForeignKeys(t *testing.T) {
	db := openTemp(t)

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
}
