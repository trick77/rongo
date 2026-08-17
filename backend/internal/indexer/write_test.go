package indexer

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/symbols"
)

const writeDim = 4

// writeDB opens a migrated database holding one repository row, because files
// reference repo_state.
func writeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, writeDim); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch) VALUES ('shop', 'file:///x', 'master')`); err != nil {
		t.Fatalf("seed repo_state: %v", err)
	}
	return db
}

func vec(marker float32) []float32 {
	v := make([]float32, writeDim)
	for i := range v {
		v[i] = marker
	}
	return v
}

func sampleChunks() []Chunk {
	return []Chunk{
		{Ordinal: 0, StartLine: 1, EndLine: 4, Symbol: "run", Text: "enriched one", RawText: "public void run() {}", TokenCount: 5, ContentHash: "h1"},
		{Ordinal: 1, StartLine: 5, EndLine: 9, Symbol: "seen", Text: "enriched two", RawText: "public int seen() { return 0; }", TokenCount: 6, ContentHash: "h2"},
	}
}

func countOf(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestReplaceFile_writesAllFourTablesWithMatchingRowids(t *testing.T) {
	// Given
	db := writeDB(t)
	testee := NewWriter(db)

	// When
	err := testee.ReplaceFile(context.Background(), "shop", "src/A.java", "abc123", "java", 64,
		sampleChunks(), [][]float32{vec(1), vec(2)},
		[]symbols.Symbol{{Name: "run", Kind: "method", Line: 1}})

	// Then
	if err != nil {
		t.Fatalf("ReplaceFile() err = %v, want nil", err)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM files WHERE repo = 'shop'`); n != 1 {
		t.Errorf("files rows = %d, want 1", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks`); n != 2 {
		t.Errorf("chunks rows = %d, want 2", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM symbols`); n != 1 {
		t.Errorf("symbols rows = %d, want 1", n)
	}
	// vec0 cannot take part in triggers or FK cascades, so this 1:1 bridge is
	// maintained by hand — and it is exactly the thing that rots silently. The
	// join is the assertion.
	joined := countOf(t, db, `
		SELECT COUNT(*) FROM chunks c
		JOIN chunks_vec v ON v.rowid = c.id
		JOIN chunks_fts f ON f.rowid = c.id`)
	if joined != 2 {
		t.Errorf("only %d of 2 chunks have a matching vector AND keyword row", joined)
	}
}

func TestReplaceFile_isAtomic(t *testing.T) {
	// Given: the second vector has the wrong width, so the write fails partway.
	// A half-written file leaves the vector lane and the keyword lane disagreeing
	// about what exists, and every later result set is quietly wrong.
	db := writeDB(t)
	testee := NewWriter(db)

	// When
	err := testee.ReplaceFile(context.Background(), "shop", "src/A.java", "abc123", "java", 64,
		sampleChunks(), [][]float32{vec(1), {1, 2}}, nil)

	// Then
	if err == nil {
		t.Fatal("ReplaceFile() err = nil, want the dimension rejected")
	}
	// The rejection must come from vec0 DURING the transaction, not from a
	// pre-flight guard: only then does this exercise the rollback at all. The
	// count guard cannot fire here — a wrong WIDTH is not a wrong COUNT.
	if !strings.Contains(err.Error(), "chunk 1") {
		t.Errorf("error = %q, want the failure to name the chunk it died on, i.e. to have happened mid-transaction", err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM files`,
		`SELECT COUNT(*) FROM chunks`,
		`SELECT COUNT(*) FROM chunks_vec`,
		`SELECT COUNT(*) FROM chunks_fts`,
	} {
		if n := countOf(t, db, q); n != 0 {
			t.Errorf("%s = %d after a failed write, want 0 — the transaction did not roll back", q, n)
		}
	}
}

func TestReplaceFile_replacesRatherThanAccumulates(t *testing.T) {
	// Given: the same path indexed twice, as every re-index does.
	db := writeDB(t)
	testee := NewWriter(db)
	ctx := context.Background()
	write := func() {
		t.Helper()
		if err := testee.ReplaceFile(ctx, "shop", "src/A.java", "abc123", "java", 64,
			sampleChunks(), [][]float32{vec(1), vec(2)},
			[]symbols.Symbol{{Name: "run", Kind: "method", Line: 1}}); err != nil {
			t.Fatalf("ReplaceFile() err = %v", err)
		}
	}
	write()

	// When
	write()

	// Then
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks`); n != 2 {
		t.Errorf("chunks = %d after two runs, want 2", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_vec`); n != 2 {
		t.Errorf("chunks_vec = %d after two runs, want 2", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts`); n != 2 {
		t.Errorf("chunks_fts = %d after two runs, want 2", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM symbols`); n != 1 {
		t.Errorf("symbols = %d after two runs, want 1", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM files`); n != 1 {
		t.Errorf("files = %d after two runs, want 1", n)
	}
}

func TestDeleteFile_clearsAllThreeChunkTables(t *testing.T) {
	// Given
	db := writeDB(t)
	testee := NewWriter(db)
	ctx := context.Background()
	if err := testee.ReplaceFile(ctx, "shop", "src/A.java", "abc123", "java", 64,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil); err != nil {
		t.Fatalf("ReplaceFile() err = %v", err)
	}

	// When
	if err := testee.DeleteFile(ctx, "shop", "src/A.java"); err != nil {
		t.Fatalf("DeleteFile() err = %v", err)
	}

	// Then: an orphaned vector row would keep answering questions about code
	// that is gone, which is the "never invent" invariant violated by omission.
	for _, q := range []string{
		`SELECT COUNT(*) FROM files`,
		`SELECT COUNT(*) FROM chunks`,
		`SELECT COUNT(*) FROM chunks_vec`,
		`SELECT COUNT(*) FROM chunks_fts`,
	} {
		if n := countOf(t, db, q); n != 0 {
			t.Errorf("%s = %d after DeleteFile, want 0", q, n)
		}
	}
	// Row count zero and index clean are not the same claim for fts5: the
	// tokens live in a separate b-tree, and a term left behind there keeps
	// answering searches about code that is gone.
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts WHERE raw_text MATCH ?`, `"seen"`); n != 0 {
		t.Errorf("a term from the deleted chunk still matches (%d hits); the fts5 index was not cleared", n)
	}
}

func TestDeleteFile_onAnUnknownPathIsNotAnError(t *testing.T) {
	// Given: a diff can name a path that was never indexed (it was skipped, or
	// added and deleted between two polls).
	db := writeDB(t)

	// When
	err := NewWriter(db).DeleteFile(context.Background(), "shop", "never/seen.java")

	// Then
	if err != nil {
		t.Errorf("DeleteFile() err = %v, want nil for a path that was never indexed", err)
	}
}

func TestRecordSkipped_keepsTheFileVisibleWithoutChunks(t *testing.T) {
	// Given: a file the selector rejected. It must still be RECORDED, so the
	// answer layer can say "that file exists but was not indexed" rather than
	// pretending it is absent.
	db := writeDB(t)
	testee := NewWriter(db)

	// When
	err := testee.RecordSkipped(context.Background(), "shop", "node_modules/x/index.js", "abc123", "javascript",
		string(SkipVendored), 42)

	// Then
	if err != nil {
		t.Fatalf("RecordSkipped() err = %v", err)
	}
	var reason string
	if err := db.QueryRow(`SELECT skip_reason FROM files WHERE path = 'node_modules/x/index.js'`).Scan(&reason); err != nil {
		t.Fatalf("the skipped file was not recorded at all: %v", err)
	}
	if reason != string(SkipVendored) {
		t.Errorf("skip_reason = %q, want %q", reason, SkipVendored)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks`); n != 0 {
		t.Errorf("chunks = %d for a skipped file, want 0", n)
	}
}

func TestRecordSkipped_clearsChunksOfAFileThatUsedToBeIndexed(t *testing.T) {
	// Given: a file that was indexed and has since grown past the size ceiling
	// or gained a secret. Its old chunks must go, or the index keeps serving
	// content the selector has decided not to index.
	db := writeDB(t)
	testee := NewWriter(db)
	ctx := context.Background()
	if err := testee.ReplaceFile(ctx, "shop", "src/A.java", "abc123", "java", 64,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil); err != nil {
		t.Fatalf("ReplaceFile() err = %v", err)
	}

	// When
	if err := testee.RecordSkipped(ctx, "shop", "src/A.java", "def456", "java", string(SkipSecret), 99); err != nil {
		t.Fatalf("RecordSkipped() err = %v", err)
	}

	// Then
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks`); n != 0 {
		t.Errorf("chunks = %d, want 0", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_vec`); n != 0 {
		t.Errorf("chunks_vec = %d, want 0", n)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_fts`); n != 0 {
		t.Errorf("chunks_fts = %d, want 0", n)
	}
}

func TestReplaceFile_rejectsAVectorCountMismatch(t *testing.T) {
	// Given: one vector for two chunks. Storing them would pair chunk 2 with
	// nothing, or worse, with chunk 1's vector.
	db := writeDB(t)

	// When
	err := NewWriter(db).ReplaceFile(context.Background(), "shop", "src/A.java", "abc", "java", 64,
		sampleChunks(), [][]float32{vec(1)}, nil)

	// Then
	if err == nil {
		t.Fatal("ReplaceFile() err = nil, want a rejection of the count mismatch")
	}
}
