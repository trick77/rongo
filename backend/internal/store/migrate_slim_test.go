package store

import (
	"database/sql"
	"strings"
	"testing"
)

const slimMigration = "0038_drop_unread.sql"

// droppedIndexes are the five 0038 takes away.
var droppedIndexes = []string{"idx_files_repo", "idx_chunks_file", "idx_messages_thread",
	"idx_citations_message", "idx_message_sources_message"}

// beforeSlim is a database as it stood before the migration.
func beforeSlim(t *testing.T) *sql.DB {
	t.Helper()
	db := openTemp(t)
	if err := migrateBefore(db, 4, slimMigration); err != nil {
		t.Fatalf("migrate up to %s: %v", slimMigration, err)
	}
	return db
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
	if err := rows.Err(); err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	return out
}

func indexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatalf("look up index %s: %v", name, err)
	}
	return n == 1
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestSlimMigration_keepsEveryChunkAndItsMirrorsOnAnIndexedDatabase(t *testing.T) {
	// Given a database as it stood before the migration, with an indexed
	// file — a chunk, its vector, its keywords — and a turn that cites it
	db := beforeSlim(t)
	for _, q := range []string{
		`INSERT INTO repo_state (name, clone_url, branch) VALUES ('peeq', 'x', 'master')`,
		`INSERT INTO files (id, repo, path, sha) VALUES (1, 'peeq', 'a.go', 'deadbeef')`,
		`INSERT INTO chunks (id, file_id, ordinal, start_line, end_line, symbol, text, raw_text, token_count, content_hash)
		 VALUES (7, 1, 0, 3, 9, 'A', 'path: a.go' || char(10) || 'func A() {}', 'func A() {}', 5, 'h7')`,
		`INSERT INTO chunks_vec (rowid, embedding) VALUES (7, '[0.1,0.2,0.3,0.4]')`,
		`INSERT INTO chunks_fts (rowid, raw_text) VALUES (7, 'func A() {}')`,
		`INSERT INTO symbols (file_id, name, kind, line) VALUES (1, 'A', 'function', 3)`,
		`INSERT INTO users (subject, email, is_admin) VALUES ('anna', '', 0)`,
		`INSERT INTO threads (id, user_subject, title) VALUES (1, 'anna', 'How?')`,
		`INSERT INTO messages (id, thread_id, ordinal, audience, question, answer) VALUES (1, 1, 0, 'ba', 'How?', 'So [1].')`,
		`INSERT INTO messages (id, thread_id, ordinal, audience, question) VALUES (2, 1, 1, 'ba', 'And then?')`,
		`INSERT INTO citations (message_id, marker, repo, branch, path, start_line, end_line, sha)
		 VALUES (1, 1, 'peeq', 'master', 'a.go', 3, 9, 'deadbeef')`,
		`INSERT INTO message_sources (message_id, chunk_id, commit_id, reason) VALUES (1, 7, 0, 'hit')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
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
		`SELECT count(*) FROM citations WHERE message_id = 1`,
		`SELECT count(*) FROM message_sources WHERE message_id = 1`,
	} {
		if n := count(t, db, q); n != 1 {
			t.Errorf("%s = %d, want 1", q, n)
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
	if n := count(t, db, `SELECT count(*) FROM chunks`); n != 0 {
		t.Errorf("chunks left after their file was deleted = %d", n)
	}
	// The record's own constraints too: a thread still has one turn per
	// position, and deleting it still takes its turns and their evidence.
	if _, err := db.Exec(`INSERT INTO messages (thread_id, ordinal, audience, question) VALUES (1, 1, 'ba', 'again')`); err == nil {
		t.Error("a second turn at the same position of a thread was accepted")
	}
	if _, err := db.Exec(`DELETE FROM threads WHERE id = 1`); err != nil {
		t.Fatalf("delete thread: %v", err)
	}
	for _, table := range []string{"messages", "citations", "message_sources"} {
		if n := count(t, db, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s left after their thread was deleted = %d", table, n)
		}
	}
}

func TestSlimMigration_dropsTheFiveIndexesThatWereThere(t *testing.T) {
	// Given the names as the schema had them: DROP INDEX IF EXISTS says
	// nothing about a name it does not find, so a misspelling here or in the
	// migration would leave the index standing and every check below green
	db := beforeSlim(t)
	for _, name := range droppedIndexes {
		if !indexExists(t, db, name) {
			t.Fatalf("index %s does not exist before the migration; the name is wrong", name)
		}
	}

	// When
	if err := Migrate(db, 4); err != nil {
		t.Fatalf("Migrate() err = %v", err)
	}

	// Then
	for _, name := range droppedIndexes {
		if indexExists(t, db, name) {
			t.Errorf("index %s is still there", name)
		}
	}
}

func TestSlimMigration_theReadsThatUsedTheDroppedIndexesStillUseOne(t *testing.T) {
	// Each dropped index was the leftmost prefix of the UNIQUE constraint on
	// the same table. These are the reads that used them: every one must
	// still be answered from an index — no table scan, and no sort added for
	// an ORDER BY the dropped index used to deliver.
	db := migratedDB(t)
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
		if err := rows.Err(); err != nil {
			t.Fatalf("explain %q: %v", q, err)
		}
		_ = rows.Close()
		joined := strings.Join(plan, " | ")
		if !strings.Contains(joined, "USING") || strings.Contains(joined, "SCAN") || strings.Contains(joined, "TEMP B-TREE") {
			t.Errorf("%q is planned as %q, want an index search and no sort", q, joined)
		}
	}
}
