package store

import "testing"

// TestVec0_returnsNearestNeighbour proves the sqlite-vec WASM build is loaded
// and functional in the ncruces driver. It is a version-skew alarm: the two
// modules are compiled against each other, so a mismatched bump breaks vector
// search at runtime rather than at compile time.
func TestVec0_returnsNearestNeighbour(t *testing.T) {
	// Given: three orthogonal unit vectors.
	db := openTemp(t)
	if _, err := db.Exec(`CREATE VIRTUAL TABLE vec_smoke USING vec0(embedding float[3])`); err != nil {
		t.Fatalf("create vec0 table: %v (is the sqlite-vec build loaded?)", err)
	}
	vectors := []struct {
		rowid int
		vec   []float32
	}{
		{1, []float32{1, 0, 0}},
		{2, []float32{0, 1, 0}},
		{3, []float32{0, 0, 1}},
	}
	for _, v := range vectors {
		if _, err := db.Exec(
			`INSERT INTO vec_smoke(rowid, embedding) VALUES (?, ?)`,
			v.rowid, VecLiteral(v.vec),
		); err != nil {
			t.Fatalf("insert rowid %d: %v", v.rowid, err)
		}
	}

	// When: querying close to the first vector. vec0 requires the `k = ?`
	// constraint to sit alongside the MATCH.
	var rowid int
	var distance float64
	err := db.QueryRow(
		`SELECT rowid, distance FROM vec_smoke
		 WHERE embedding MATCH ? AND k = ?
		 ORDER BY distance`,
		VecLiteral([]float32{0.9, 0.1, 0}), 1,
	).Scan(&rowid, &distance)

	// Then
	if err != nil {
		t.Fatalf("knn query: %v", err)
	}
	if rowid != 1 {
		t.Errorf("nearest rowid = %d, want 1", rowid)
	}
	if distance <= 0 || distance > 1 {
		t.Errorf("distance = %f, want a small positive L2 distance", distance)
	}
}

// TestFTS5_isAvailable proves FTS5 is compiled into the same WASM build. The
// hybrid retriever in phase 2 depends on both lanes living in one driver.
func TestFTS5_isAvailable(t *testing.T) {
	// Given
	db := openTemp(t)
	if _, err := db.Exec(`CREATE VIRTUAL TABLE fts_smoke USING fts5(text)`); err != nil {
		t.Fatalf("create fts5 table: %v (is FTS5 compiled in?)", err)
	}
	if _, err := db.Exec(
		`INSERT INTO fts_smoke(rowid, text) VALUES (1, 'teaser mail versand'), (2, 'gutschein ablauf')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// When
	var rowid int
	err := db.QueryRow(`SELECT rowid FROM fts_smoke WHERE text MATCH 'teaser'`).Scan(&rowid)

	// Then
	if err != nil {
		t.Fatalf("match query: %v", err)
	}
	if rowid != 1 {
		t.Errorf("rowid = %d, want 1", rowid)
	}
}
