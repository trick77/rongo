package store

import (
	"database/sql"
	"strings"
	"testing"
)

// backfillSQL is 0007's second statement, the recursive walk that gives rows
// written before the column existed the head they always belonged to. Taken
// from the migration itself rather than copied: a test that re-types the SQL
// stops testing the migration the moment one of the two is edited.
func backfillSQL(t *testing.T) string {
	t.Helper()
	body, err := migrationsFS.ReadFile("migrations/0007_message_head.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	i := strings.Index(string(body), "WITH RECURSIVE")
	if i < 0 {
		t.Fatal("0007 no longer contains the backfill walk")
	}
	return string(body)[i:]
}

// seedResume writes a message and, on it, a clarification another message
// resumed. Returns the resuming message's id.
func seedResume(t *testing.T, db *sql.DB, threadID, ordinal int64, question string, cardOn int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO clarifications (message_id, understanding) VALUES (?, '{}')`, cardOn)
	if err != nil {
		t.Fatalf("seed clarification: %v", err)
	}
	clarID, _ := res.LastInsertId()
	res, err = db.Exec(`
		INSERT INTO messages (thread_id, ordinal, audience, language, question, from_clarification_id, from_candidate_idx)
		VALUES (?, ?, 'ba', 'en', ?, ?, 0)`, threadID, ordinal, question, clarID)
	if err != nil {
		t.Fatalf("seed resume: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedThreadAndHead(t *testing.T, db *sql.DB, question string) (int64, int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO users (subject) VALUES ('anna')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	res, err := db.Exec(`INSERT INTO threads (user_subject, title) VALUES ('anna', 't')`)
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	threadID, _ := res.LastInsertId()
	res, err = db.Exec(`
		INSERT INTO messages (thread_id, ordinal, audience, language, question)
		VALUES (?, 0, 'ba', 'en', ?)`, threadID, question)
	if err != nil {
		t.Fatalf("seed head: %v", err)
	}
	headID, _ := res.LastInsertId()
	return threadID, headID
}

func headOf(t *testing.T, db *sql.DB, id int64) int64 {
	t.Helper()
	var h int64
	if err := db.QueryRow(`SELECT head_message_id FROM messages WHERE id = ?`, id).Scan(&h); err != nil {
		t.Fatalf("read head: %v", err)
	}
	return h
}

// migratedDB is a fresh database with every migration applied.
func migratedDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTemp(t)
	if err := Migrate(db, 1536); err != nil {
		t.Fatalf("Migrate() err = %v", err)
	}
	return db
}

// A card can be answered, and the resumed turn can end in a card of its own,
// so the backfill has to reach the root rather than the row one step up.
func TestBackfillWalksToTheRoot(t *testing.T) {
	db := migratedDB(t)
	threadID, head := seedThreadAndHead(t, db, "wie unterscheidet sich das?")
	first := seedResume(t, db, threadID, 1, "wie unterscheidet sich das?", head)
	second := seedResume(t, db, threadID, 2, "wie unterscheidet sich das?", first)

	// The rows are seeded as they would have been written before 0007 existed.
	if _, err := db.Exec(`UPDATE messages SET head_message_id = 0`); err != nil {
		t.Fatalf("clear heads: %v", err)
	}
	if _, err := db.Exec(backfillSQL(t)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if got := headOf(t, db, first); got != head {
		t.Errorf("first resume head = %d, want %d", got, head)
	}
	// Not `first`: the head is the question, and the second resume is a third
	// attempt at it, not an attempt at the second attempt.
	if got := headOf(t, db, second); got != head {
		t.Errorf("second resume head = %d, want %d", got, head)
	}
	if got := headOf(t, db, head); got != 0 {
		t.Errorf("head row head = %d, want 0 (it IS the head)", got)
	}
}

// A retry or a re-explain written before 0007 left nothing behind that tells
// it apart from a reader who typed the same question again, so the backfill
// leaves it alone rather than regrouping the record on a hunch.
func TestBackfillLeavesUnlinkedRowsAlone(t *testing.T) {
	db := migratedDB(t)
	threadID, head := seedThreadAndHead(t, db, "frage")
	res, err := db.Exec(`
		INSERT INTO messages (thread_id, ordinal, audience, language, question)
		VALUES (?, 1, 'dev', 'en', 'frage')`, threadID)
	if err != nil {
		t.Fatalf("seed re-explain: %v", err)
	}
	reexplain, _ := res.LastInsertId()

	if _, err := db.Exec(`UPDATE messages SET head_message_id = 0`); err != nil {
		t.Fatalf("clear heads: %v", err)
	}
	if _, err := db.Exec(backfillSQL(t)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if got := headOf(t, db, reexplain); got != 0 {
		t.Errorf("unlinked row head = %d, want 0 (left where it was)", got)
	}
	if got := headOf(t, db, head); got != 0 {
		t.Errorf("head row head = %d, want 0", got)
	}
}
