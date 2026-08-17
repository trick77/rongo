package store

import (
	"database/sql"
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
	if err := Migrate(db); err != nil {
		t.Fatalf("first Migrate() err = %v", err)
	}
	if err := Migrate(db); err != nil {
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
	if err := Migrate(db); err != nil {
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
