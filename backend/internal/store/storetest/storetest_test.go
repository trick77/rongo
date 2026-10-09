package storetest

import (
	"testing"

	"github.com/trick77/rongo/internal/store"
)

func TestOpen_isMigratedAtTheDimensionAsked(t *testing.T) {
	db := Open(t, 4)
	dim, err := store.BuiltDim(db)
	if err != nil {
		t.Fatal(err)
	}
	if dim != 4 {
		t.Errorf("dim = %d, want 4", dim)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM repo_state`).Scan(&n); err != nil {
		t.Fatalf("repo_state: %v", err)
	}
	if n != 0 {
		t.Errorf("repo_state rows = %d, want an empty index", n)
	}
}

func TestOpen_twoCallsAreTwoDatabases(t *testing.T) {
	a, b := Open(t, 4), Open(t, 4)
	if _, err := a.Exec(`INSERT INTO repo_state (name, clone_url) VALUES ('x', 'file:///x')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.QueryRow(`SELECT count(*) FROM repo_state`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second database saw %d rows of the first", n)
	}
}
