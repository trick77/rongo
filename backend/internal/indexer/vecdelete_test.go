package indexer

import (
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

func TestDeleteVecRow_isPointLookup(t *testing.T) {
	if plan := vecPlan(t, deleteVecRow, 1); !vec0PointPlan.MatchString(plan) {
		t.Fatalf("deleteVecRow must be a vec0 point lookup, plan:\n%s", plan)
	}
	if plan := vecPlan(t, `DELETE FROM chunks_vec WHERE rowid IN (SELECT id FROM chunks WHERE file_id = ?)`, 1); !vec0Fullscan.MatchString(plan) {
		t.Fatalf("rowid IN should show the fullscan this guards against, plan:\n%s", plan)
	}
}
