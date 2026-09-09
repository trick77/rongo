package retrieve

import (
	"context"
	"database/sql"
	"testing"
)

// park sets enabled = 0 the way SyncSpecs does for `enabled: false`.
func park(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = ?`, name); err != nil {
		t.Fatalf("park %s: %v", name, err)
	}
}

// TestSearchVector_parkedRepoIsPreFiltered: the trap this whole filter has to
// avoid. chunks_vec is a top-k operator, so an `enabled = 1` predicate sitting
// in the JOIN would run AFTER the KNN — the parked repository's chunks would
// win the k slots and then be discarded, and a live repository holding a small
// slice of the corpus would return nothing at all. Same shape as
// TestSearchVector_repoFilterIsAPreFilter, and the same fix.
func TestSearchVector_parkedRepoIsPreFiltered(t *testing.T) {
	// Given: the global top-2 is entirely the parked repository
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addRepo(t, db, "loom", "main")
	addChunk(t, db, "peeq", "a.go", "A", "alpha", nearVec)
	addChunk(t, db, "peeq", "b.go", "B", "bravo", nearVec)
	addChunk(t, db, "peeq", "c.go", "C", "charlie", nearVec)
	addChunk(t, db, "loom", "d.go", "D", "delta", midVec)
	park(t, db, "peeq")

	// When: no repository restriction at all
	hits, err := NewStore(db).SearchVector(context.Background(), queryVec, 2, DefaultMaxDistance, nil)

	// Then
	if err != nil {
		t.Fatalf("SearchVector() err = %v", err)
	}
	if len(hits) != 1 || hits[0].Repo != "loom" {
		t.Errorf("SearchVector() = %v, want loom's chunk — the parked repo took the k slots", hits)
	}
}

// TestSearchKeyword_parkedRepoIsNotReturned: FTS5 is not a top-k operator, so
// the join predicate is a pre-filter by construction. The claim to pin is
// simply that a parked repository never reaches an answer.
func TestSearchKeyword_parkedRepoIsNotReturned(t *testing.T) {
	// Given
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addRepo(t, db, "loom", "main")
	addChunk(t, db, "peeq", "a.go", "A", "sender.send()", nearVec)
	addChunk(t, db, "loom", "d.go", "D", "sender.send()", midVec)
	park(t, db, "peeq")

	// When
	hits, err := NewStore(db).SearchKeyword(context.Background(), "send", 10, nil)

	// Then
	if err != nil {
		t.Fatalf("SearchKeyword() err = %v", err)
	}
	if len(hits) != 1 || hits[0].Repo != "loom" {
		t.Errorf("SearchKeyword() = %v, want only loom's chunk", hits)
	}
}

// TestSearch_parkedRepoIsInvisibleToTheWholePipeline: enabled means one thing
// everywhere. Before this, `enabled: false` stopped the poller and nothing
// else — the chunks stayed in both mirrors and went on being retrieved and
// cited out of an index the Repos page said was parked.
func TestSearch_parkedRepoIsInvisibleToTheWholePipeline(t *testing.T) {
	// Given
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addChunk(t, db, "peeq", "a.go", "A", "sender.send()", nearVec)
	park(t, db, "peeq")
	r := New(db, fixedEmbedder{vec: queryVec})

	// When
	hits, err := r.Search(context.Background(), Query{Text: "sender.send()", K: 10})

	// Then
	if err != nil {
		t.Fatalf("Search() err = %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("Search() = %v, want nothing — a parked repository answers no questions", hits)
	}
}

// TestKnownRepos_parkedRepoReadsAsUnknown: naming a parked repository must take
// the SAME path as naming one that was never indexed — the name is dropped from
// the restriction and the turn says so out loud. Reusing that path is the point:
// a restriction that kept the name would match nothing and report "nothing
// found" about a whole corpus.
func TestKnownRepos_parkedRepoReadsAsUnknown(t *testing.T) {
	// Given
	db := testDB(t)
	addRepo(t, db, "peeq", "master")
	addRepo(t, db, "loom", "main")
	addChunk(t, db, "peeq", "a.go", "A", "alpha", nearVec)
	addChunk(t, db, "loom", "d.go", "D", "delta", midVec)
	park(t, db, "peeq")
	r := New(db, fixedEmbedder{vec: queryVec})

	// When
	got, err := r.knownRepos(context.Background(), []string{"peeq", "loom"}, "")

	// Then
	if err != nil {
		t.Fatalf("knownRepos() err = %v", err)
	}
	if len(got) != 1 || got[0] != "loom" {
		t.Errorf("knownRepos() = %v, want [loom] — peeq is parked and reads as unknown", got)
	}
}
