package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// Migration 0022 backfills last_indexed_at from last_run_at. It has to read
// "an index exists" off the counts, not off last_sha: 0021 empties last_sha
// on every row, and an install applying both in one boot would otherwise
// backfill nothing and show "never" beside thousands of chunks until its
// next successful poll.
func TestMigrate_0022BackfillsIndexedFromTheCountsNotTheSHA(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The state 0021 leaves behind: an indexed repository with no sha, and
	// one that was never indexed at all.
	for _, row := range []struct {
		name   string
		chunks int
	}{{"shop", 3120}, {"empty", 0}} {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, last_sha, last_run_at, chunk_count, last_indexed_at)
			VALUES (?, 'file:///x', '', '2026-09-13T09:30:00Z', ?, '')`, row.name, row.chunks); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// When: the backfill statement the migration file carries runs again.
	body, err := migrationsFS.ReadFile("migrations/0022_last_indexed_at.sql")
	if err != nil {
		t.Fatal(err)
	}
	var update string
	for _, stmt := range strings.Split(string(body), ";") {
		if strings.Contains(stmt, "UPDATE repo_state") {
			update = stmt
		}
	}
	if update == "" {
		t.Fatal("no UPDATE in the migration")
	}
	if _, err := db.Exec(update); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Then
	got := map[string]string{}
	rows, err := db.Query(`SELECT name, last_indexed_at FROM repo_state`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, at string
		if err := rows.Scan(&name, &at); err != nil {
			t.Fatal(err)
		}
		got[name] = at
	}
	if got["shop"] != "2026-09-13T09:30:00Z" {
		t.Errorf("shop last_indexed_at = %q, want the last poll time backfilled", got["shop"])
	}
	if got["empty"] != "" {
		t.Errorf("empty last_indexed_at = %q, want none for a repository never indexed", got["empty"])
	}
}
