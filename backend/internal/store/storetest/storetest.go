// Package storetest opens the migrated temporary database every store-backed
// test starts from. Test-only: nothing outside a _test.go file imports it.
// store's own tests cannot use it (import cycle) and keep their openers.
package storetest

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

// Open opens a fresh database under the test's temp dir, migrated for vectors
// of dim dimensions, and closes it when the test ends. Seeding stays with the
// caller: what a package needs in repo_state or users is its own fixture.
func Open(tb testing.TB, dim int) *sql.DB {
	tb.Helper()
	db, err := store.Open(filepath.Join(tb.TempDir(), "rongo.db"))
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db, dim); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	return db
}
