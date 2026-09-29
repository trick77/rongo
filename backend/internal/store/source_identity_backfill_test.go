package store

import (
	"strings"
	"testing"
)

// sourceIdentityBackfill is 0037's UPDATE statements, taken from the
// migration itself so the test cannot drift from it.
func sourceIdentityBackfill(t *testing.T) string {
	t.Helper()
	body, err := migrationsFS.ReadFile("migrations/0037_source_identity.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	i := strings.Index(string(body), "UPDATE message_sources")
	if i < 0 {
		t.Fatal("0037 no longer contains the backfill")
	}
	return string(body)[i:]
}

func TestSourceIdentityBackfill_completesRowsThatStillJoinAndLeavesTheRest(t *testing.T) {
	db := migratedDB(t)
	_, headID := seedThreadAndHead(t, db, "q")
	for _, q := range []string{
		`INSERT INTO repo_state (name, clone_url, branch) VALUES ('peeq', 'x', 'master')`,
		`INSERT INTO files (id, repo, path, sha) VALUES (1, 'peeq', 'a.go', 'deadbeef')`,
		`INSERT INTO chunks (id, file_id, ordinal, start_line, end_line, symbol, text, raw_text, content_hash)
		 VALUES (7, 1, 0, 3, 9, 'A', 't', 't', 'h')`,
		`INSERT INTO commits (id, repo, sha, committed_at, author, subject, body, paths)
		 VALUES (5, 'peeq', 'aaa1111', '2026-09-17T10:00:00Z', 'x', 's', 'b', '')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for _, row := range [][2]int64{{7, 0}, {8, 0}, {0, 5}} {
		if _, err := db.Exec(`INSERT INTO message_sources (message_id, chunk_id, commit_id, reason) VALUES (?, ?, ?, 'hit')`,
			headID, row[0], row[1]); err != nil {
			t.Fatalf("seed source: %v", err)
		}
	}

	if _, err := db.Exec(sourceIdentityBackfill(t)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	type ident struct {
		repo, path, sha, symbol string
		start, end              int
	}
	read := func(chunk, commit int64) ident {
		var i ident
		if err := db.QueryRow(`SELECT repo, path, sha, symbol, start_line, end_line FROM message_sources
			WHERE chunk_id = ? AND commit_id = ?`, chunk, commit).Scan(&i.repo, &i.path, &i.sha, &i.symbol, &i.start, &i.end); err != nil {
			t.Fatalf("read %d/%d: %v", chunk, commit, err)
		}
		return i
	}
	if got := read(7, 0); got != (ident{"peeq", "a.go", "deadbeef", "A", 3, 9}) {
		t.Errorf("chunk row = %+v", got)
	}
	if got := read(8, 0); got != (ident{}) {
		t.Errorf("a row whose chunk is gone = %+v, want it left alone", got)
	}
	if got := read(0, 5); got != (ident{repo: "peeq", sha: "aaa1111"}) {
		t.Errorf("commit row = %+v", got)
	}
}
