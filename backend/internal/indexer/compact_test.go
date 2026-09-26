package indexer

import (
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

// churnVectors inserts n vectors and deletes all but the last keep, the way
// re-indexing does: vec0 clears a deleted row's validity bit and never frees
// its chunk, and inserts only append to the newest chunk.
func churnVectors(t *testing.T, db *sql.DB, n, keep int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if _, err := tx.Exec(`INSERT INTO chunks_vec (rowid, embedding) VALUES (?, ?)`,
			i, store.VecLiteral(vec(float32(i)))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM chunks_vec WHERE rowid <= ?`, n-keep); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func nearest(t *testing.T, db *sql.DB, marker float32, k int) []int64 {
	t.Helper()
	rows, err := db.Query(`SELECT rowid FROM chunks_vec WHERE embedding MATCH ? AND k = ? ORDER BY distance`,
		store.VecLiteral(vec(marker)), k)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestCompactVectors_dropsTheChunksDeletesLeftBehind(t *testing.T) {
	db := writeDB(t)
	churnVectors(t, db, 3000, 100)
	before := nearest(t, db, 2950, 10)

	got, err := CompactVectors(context.Background(), db)
	if err != nil {
		t.Fatalf("CompactVectors() err = %v", err)
	}

	want := VecCompaction{Compacted: true, Rows: 100, ChunksBefore: 3, ChunksAfter: 1}
	if got.Compacted != want.Compacted || got.Rows != want.Rows ||
		got.ChunksBefore != want.ChunksBefore || got.ChunksAfter != want.ChunksAfter {
		t.Errorf("CompactVectors() = %+v, want %+v", got, want)
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_vec_chunks`); n != 1 {
		t.Errorf("chunks_vec_chunks = %d, want 1", n)
	}
	// Same rowids, same vectors: retrieval joins chunks_vec to chunks on rowid.
	if after := nearest(t, db, 2950, 10); !slices.Equal(after, before) {
		t.Errorf("nearest after compaction = %v, want %v", after, before)
	}
	if dim, err := store.BuiltDim(db); err != nil || dim != writeDim {
		t.Errorf("BuiltDim() = %d, %v, want %d", dim, err, writeDim)
	}
}

func TestCompactVectors_leavesAHealthyTableAlone(t *testing.T) {
	db := writeDB(t)
	churnVectors(t, db, 1500, 1400)

	got, err := CompactVectors(context.Background(), db)
	if err != nil {
		t.Fatalf("CompactVectors() err = %v", err)
	}
	if got.Compacted || got.ChunksBefore != 2 || got.Rows != 1400 {
		t.Errorf("CompactVectors() = %+v, want untouched with 2 chunks and 1400 rows", got)
	}
}

func TestCompactVectors_anEmptyTableIsNotBloated(t *testing.T) {
	db := writeDB(t)
	got, err := CompactVectors(context.Background(), db)
	if err != nil || got.Compacted {
		t.Errorf("CompactVectors() = %+v, %v, want untouched", got, err)
	}
}

func TestCompactVectorsAndLog_saysWhatItDid(t *testing.T) {
	db := writeDB(t)
	churnVectors(t, db, 3000, 100)
	logs := &capture{}

	CompactVectorsAndLog(context.Background(), db, slog.New(logs), "repo", "shop")

	r, ok := logs.find("vector index compacted")
	if !ok {
		t.Fatalf("no compaction line, records = %v", logs.records)
	}
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	for k, v := range map[string]string{"repo": "shop", "rows": "100", "chunks_before": "3", "chunks_after": "1"} {
		if attrs[k] != v {
			t.Errorf("attr %s = %q, want %q (all: %v)", k, attrs[k], v, attrs)
		}
	}
	if attrs["took"] == "" {
		t.Errorf("no took attr: %v", attrs)
	}
}

func TestCompactVectorsAndLog_quietWhenHealthy(t *testing.T) {
	db := writeDB(t)
	churnVectors(t, db, 10, 10)
	logs := &capture{}

	CompactVectorsAndLog(context.Background(), db, slog.New(logs))

	if len(logs.records) != 0 {
		t.Errorf("records = %v, want none", logs.records)
	}
}

func TestCompactVectorsAndLog_aFailureIsAWarning(t *testing.T) {
	db := writeDB(t)
	churnVectors(t, db, 3000, 100)
	logs := &capture{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	CompactVectorsAndLog(ctx, db, slog.New(logs))

	r, ok := logs.find("compacting the vector index failed")
	if !ok || r.Level != slog.LevelWarn {
		t.Fatalf("want a warning, records = %v", logs.records)
	}
	// Nothing changed: the transaction rolled back whole.
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks_vec_rowids`); n != 100 {
		t.Errorf("rows = %d, want 100", n)
	}
}

func TestLogVectorIndexAtBoot_compactsAndVacuumsABloatedTable(t *testing.T) {
	db := writeDB(t)
	churnVectors(t, db, 3000, 100)
	logs := &capture{}

	LogVectorIndexAtBoot(context.Background(), db, slog.New(logs))

	for _, msg := range []string{"vector index compacted", "database vacuumed"} {
		if _, ok := logs.find(msg); !ok {
			t.Errorf("no %q line, records = %v", msg, logs.records)
		}
	}
}

func TestLogVectorIndexAtBoot_statesAHealthyTable(t *testing.T) {
	db := writeDB(t)
	churnVectors(t, db, 10, 10)
	logs := &capture{}

	LogVectorIndexAtBoot(context.Background(), db, slog.New(logs))

	r, ok := logs.find("vector index")
	if !ok {
		t.Fatalf("no vector index line, records = %v", logs.records)
	}
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	if attrs["rows"] != "10" || attrs["chunks"] != "1" {
		t.Errorf("attrs = %v, want rows=10 chunks=1", attrs)
	}
	if _, ok := logs.find("database vacuumed"); ok {
		t.Error("a healthy table was vacuumed")
	}
}

func TestLogVectorIndexAtBoot_aFailedCheckOnlyWarns(t *testing.T) {
	db := writeDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logs := &capture{}

	LogVectorIndexAtBoot(ctx, db, slog.New(logs))

	if len(logs.records) != 1 || logs.records[0].Level != slog.LevelWarn {
		t.Errorf("records = %v, want one warning", logs.records)
	}
}

func TestVacuumAndLog_reportsTheFileShrinking(t *testing.T) {
	db := writeDB(t)
	churnVectors(t, db, 3000, 100)
	if _, err := CompactVectors(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	logs := &capture{}

	VacuumAndLog(context.Background(), db, slog.New(logs))

	r, ok := logs.find("database vacuumed")
	if !ok {
		t.Fatalf("no vacuum line, records = %v", logs.records)
	}
	var before, after int64
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "bytes_before":
			before = a.Value.Int64()
		case "bytes_after":
			after = a.Value.Int64()
		}
		return true
	})
	if after <= 0 || after >= before {
		t.Errorf("bytes_before = %d, bytes_after = %d, want it to shrink", before, after)
	}
}
