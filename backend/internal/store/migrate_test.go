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
