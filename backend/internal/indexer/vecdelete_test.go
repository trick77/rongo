package indexer

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// vec0's idxStr opens with its plan: '1' fullscan, '2' point lookup. A
// `rowid IN (…)` outside KNN falls back to a fullscan of every vector.
var (
	vec0PointPlan = regexp.MustCompile(`chunks_vec VIRTUAL TABLE INDEX \d+:2`)
	vec0Fullscan  = regexp.MustCompile(`chunks_vec VIRTUAL TABLE INDEX \d+:1`)
)

func vecPlan(t *testing.T, query string, args ...any) string {
	t.Helper()
	db := writeDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return strings.Join(plan, "\n")
}

// TestDeleteVecRows_reportsEveryFailure: a vector delete that fails silently
// leaves an orphaned vector answering questions about code that is gone.
func TestDeleteVecRows_reportsEveryFailure(t *testing.T) {
	for _, tc := range []struct {
		name, drop, query string
	}{
		{"bad id query", "", `SELECT id FROM no_such_table`},
		{"id that is not an integer", "", `SELECT 'x'`},
		{"vector delete", "chunks_vec", `SELECT 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := writeDB(t)
			if tc.drop != "" {
				if _, err := db.Exec(`DROP TABLE ` + tc.drop); err != nil {
					t.Fatalf("drop: %v", err)
				}
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback() }()
			if err := deleteVecRows(context.Background(), tx, tc.query); err == nil {
				t.Error("deleteVecRows() err = nil, want the failure")
			}
		})
	}
}

// TestWriter_failsWhenAMirrorCannotBeCleared: DeleteFile and ReplaceFile must
// abort, never commit chunks whose mirror rows were left behind.
func TestWriter_failsWhenAMirrorCannotBeCleared(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, drop string
		run        func(*Writer) error
	}{
		{"DeleteFile without chunks_fts", "chunks_fts", func(w *Writer) error { return w.DeleteFile(ctx, "shop", "src/A.java") }},
		{"DeleteFile without chunks_vec", "chunks_vec", func(w *Writer) error { return w.DeleteFile(ctx, "shop", "src/A.java") }},
		{"ReplaceFile without chunks_vec", "chunks_vec", func(w *Writer) error {
			return w.ReplaceFile(ctx, "shop", "src/A.java", "def456", "java", 64,
				sampleChunks(), [][]float32{vec(1), vec(2)}, nil, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := writeDB(t)
			testee := NewWriter(db)
			if err := testee.ReplaceFile(ctx, "shop", "src/A.java", "abc123", "java", 64,
				sampleChunks(), [][]float32{vec(1), vec(2)}, nil, nil); err != nil {
				t.Fatalf("ReplaceFile() err = %v", err)
			}
			if _, err := db.Exec(`DROP TABLE ` + tc.drop); err != nil {
				t.Fatalf("drop: %v", err)
			}
			if err := tc.run(testee); err == nil {
				t.Error("err = nil, want the mirror failure")
			}
		})
	}
}

func TestDeleteVecRow_isPointLookup(t *testing.T) {
	if plan := vecPlan(t, deleteVecRow, 1); !vec0PointPlan.MatchString(plan) {
		t.Fatalf("deleteVecRow must be a vec0 point lookup, plan:\n%s", plan)
	}
	if plan := vecPlan(t, `DELETE FROM chunks_vec WHERE rowid IN (SELECT id FROM chunks WHERE file_id = ?)`, 1); !vec0Fullscan.MatchString(plan) {
		t.Fatalf("rowid IN should show the fullscan this guards against, plan:\n%s", plan)
	}
}
