package store

import (
	"database/sql"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const slimMigration = "0038_drop_unread.sql"

// migrateBefore applies every migration that sorts before stop, the way
// Migrate does, so a test can put rows into the schema a database had before
// stop and then run Migrate over it.
func migrateBefore(t *testing.T, db *sql.DB, stop string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if name >= stop {
			break
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := db.Exec(strings.ReplaceAll(string(body), embedDimPlaceholder, strconv.Itoa(4))); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
}

func columnsOf(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		out[name] = true
	}
	return out
}

func TestSlimMigration_keepsEveryChunkAndItsMirrorsOnAnIndexedDatabase(t *testing.T) {
	// Given a database as it stood before the migration, with an indexed
	// file: a chunk, its vector, its keywords, a turn that cites it
	db := openTemp(t)
	migrateBefore(t, db, slimMigration)
	for _, q := range []string{
		`INSERT INTO repo_state (name, clone_url, branch) VALUES ('peeq', 'x', 'master')`,
		`INSERT INTO files (id, repo, path, sha) VALUES (1, 'peeq', 'a.go', 'deadbeef')`,
		`INSERT INTO chunks (id, file_id, ordinal, start_line, end_line, symbol, text, raw_text, token_count, content_hash)
		 VALUES (7, 1, 0, 3, 9, 'A', 'path: a.go' || char(10) || 'func A() {}', 'func A() {}', 5, 'h7')`,
		`INSERT INTO chunks_vec (rowid, embedding) VALUES (7, '[0.1,0.2,0.3,0.4]')`,
		`INSERT INTO chunks_fts (rowid, raw_text) VALUES (7, 'func A() {}')`,
		`INSERT INTO symbols (file_id, name, kind, line) VALUES (1, 'A', 'function', 3)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// When
	if err := Migrate(db, 4); err != nil {
		t.Fatalf("Migrate() err = %v", err)
	}

	// Then the chunk is the row it was — same id, same source, same hash —
	// so its vector, its keywords and the embedding cache still belong to it
	var symbol, raw, hash string
	var fileID, start, end int
	if err := db.QueryRow(`SELECT file_id, start_line, end_line, symbol, raw_text, content_hash FROM chunks WHERE id = 7`).
		Scan(&fileID, &start, &end, &symbol, &raw, &hash); err != nil {
		t.Fatalf("read chunk: %v", err)
	}
	if fileID != 1 || start != 3 || end != 9 || symbol != "A" || raw != "func A() {}" || hash != "h7" {
		t.Errorf("chunk = file %d lines %d-%d %q %q %q, want it untouched", fileID, start, end, symbol, raw, hash)
	}
	for _, q := range []string{
		`SELECT count(*) FROM chunks_vec WHERE rowid = 7`,
		`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH 'func'`,
		`SELECT count(*) FROM symbols WHERE file_id = 1`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s = %d, err %v; want 1", q, n, err)
		}
	}
	// And the two columns nothing read are gone.
	cols := columnsOf(t, db, "chunks")
	if cols["text"] || cols["token_count"] {
		t.Errorf("chunks still has text=%v token_count=%v", cols["text"], cols["token_count"])
	}
	if !cols["raw_text"] || !cols["content_hash"] {
		t.Errorf("chunks lost a column it needs: %v", cols)
	}
	// Still unique per file and position, and still cascading with its file.
	if _, err := db.Exec(`INSERT INTO chunks (file_id, ordinal, start_line, end_line, raw_text, content_hash) VALUES (1, 0, 1, 2, 'x', 'h')`); err == nil {
		t.Error("a second chunk at the same file and ordinal was accepted")
	}
	if _, err := db.Exec(`DELETE FROM files WHERE id = 1`); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM chunks`).Scan(&left); err != nil || left != 0 {
		t.Errorf("chunks left after their file was deleted = %d, err %v", left, err)
	}
}

func TestSlimMigration_theReadsThatUsedTheDroppedIndexesStillUseOne(t *testing.T) {
	// Each dropped index was the leftmost prefix of the UNIQUE constraint on
	// the same table. These are the reads that used them: every one must
	// still be answered from an index, not by scanning the table.
	db := migratedDB(t)
	for _, name := range []string{"idx_files_repo", "idx_chunks_file", "idx_messages_thread",
		"idx_citations_message", "idx_message_sources_message"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil || n != 0 {
			t.Errorf("index %s: count = %d, err %v; want it dropped", name, n, err)
		}
	}
	for _, q := range []string{
		`SELECT path FROM files WHERE repo = 'peeq'`,
		`SELECT id FROM chunks WHERE file_id = 1`,
		`SELECT id FROM messages WHERE thread_id = 1 ORDER BY ordinal`,
		`SELECT marker FROM citations WHERE message_id = 1 ORDER BY marker`,
		`SELECT chunk_id FROM message_sources WHERE message_id = 1`,
		`DELETE FROM chunks WHERE file_id = 1`,
	} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatalf("explain %q: %v", q, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatalf("scan plan: %v", err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		joined := strings.Join(plan, " | ")
		if !strings.Contains(joined, "USING") || strings.Contains(joined, "SCAN") {
			t.Errorf("%q is planned as %q, want an index search", q, joined)
		}
	}
}
