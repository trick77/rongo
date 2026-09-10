package edges

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

const testDim = 4

// edgeDB opens a migrated database seeded with the repositories a test names.
// The rows are written directly rather than through the indexer: what is under
// test here is the lookup, and driving a real index would need ctags, git and
// an embedding endpoint to prove something about two SQL queries.
func edgeDB(t *testing.T, repos ...string) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, testDim); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, name := range repos {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url) VALUES (?, 'file:///x')`, name); err != nil {
			t.Fatalf("seed repo_state %s: %v", name, err)
		}
	}
	return db
}

// seedFileWithTokens inserts one file, one chunk holding text, its defined
// symbols, and its integration tokens.
func seedFileWithTokens(t *testing.T, db *sql.DB, repo, path, text string, syms []string, toks []Token) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO files (repo, path, sha, skip_reason) VALUES (?, ?, 'deadbeef', '')`, repo, path)
	if err != nil {
		t.Fatalf("seed file %s/%s: %v", repo, path, err)
	}
	fileID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("file id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO chunks (file_id, ordinal, start_line, end_line, text, raw_text, content_hash)
		VALUES (?, 0, 1, 50, ?, ?, ?)`, fileID, text, text, repo+"/"+path); err != nil {
		t.Fatalf("seed chunk %s/%s: %v", repo, path, err)
	}
	for _, s := range syms {
		if _, err := db.Exec(
			`INSERT INTO symbols (file_id, name, kind, line, scope) VALUES (?, ?, 'class', 1, '')`,
			fileID, s); err != nil {
			t.Fatalf("seed symbol %s: %v", s, err)
		}
	}
	for _, tok := range toks {
		if _, err := db.Exec(
			`INSERT INTO integration_tokens (file_id, kind, value, line) VALUES (?, ?, ?, ?)`,
			fileID, string(tok.Kind), tok.Value, tok.Line); err != nil {
			t.Fatalf("seed token %s: %v", tok.Value, err)
		}
	}
	return fileID
}
