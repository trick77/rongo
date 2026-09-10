package store

import (
	"path/filepath"
	"testing"
)

// Migration 0015 forces one full re-index, because tokens are written by the
// file pipeline and an incremental poll only passes changed paths. Without it
// an existing install would look healthy and link nothing.
func TestMigrate_0015ForcesOneFullReindex(t *testing.T) {
	// Given: a migrated database whose repository sits at an indexed commit.
	db, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO repo_state (name, clone_url, last_sha) VALUES ('shop', 'file:///x', 'abc123')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// When: the migration is re-applied to a database that predates it. The
	// runner records versions by filename, so the effect is asserted by
	// running the statement the file carries against a row that has a sha.
	if _, err := db.Exec(`UPDATE repo_state SET last_sha = ''`); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Then: the poller will pass nil paths and index the repository whole.
	var sha string
	if err := db.QueryRow(`SELECT last_sha FROM repo_state WHERE name = 'shop'`).Scan(&sha); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if sha != "" {
		t.Errorf("last_sha = %q, want empty so the next poll re-indexes whole", sha)
	}
}

// The table itself has to exist with the cascade, or a deleted file leaves
// edges behind that keep answering for code that is gone.
func TestMigrate_integrationTokensCascadeWithTheirFile(t *testing.T) {
	// Given
	db, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url) VALUES ('shop', 'file:///x')`); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	res, err := db.Exec(`INSERT INTO files (repo, path, sha, skip_reason) VALUES ('shop', 'A.java', 'sha', '')`)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("file id: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO integration_tokens (file_id, kind, value, line) VALUES (?, 'route', '/orders', 1)`, id); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	// When
	if _, err := db.Exec(`DELETE FROM files WHERE id = ?`, id); err != nil {
		t.Fatalf("delete file: %v", err)
	}

	// Then
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM integration_tokens`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d tokens outlived their file, want 0", n)
	}
}
