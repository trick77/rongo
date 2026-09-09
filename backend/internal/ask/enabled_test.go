package ask

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// TestGather_doesNotHopIntoAParkedRepository: the same fixture as
// TestGather_stillCrossesToAnotherRepositoryForASymbolItDoesNotDefine, with the
// target parked. AGENTS.md requires a hop target to be indexed; a repository the
// reader cannot see on the Repos page is not one to pull code out of on rongo's
// own initiative, and the retrieval lanes would never have offered it either.
func TestGather_doesNotHopIntoAParkedRepository(t *testing.T) {
	// Given
	db := gatherDB(t)
	seedRepo(t, db, "go-sqlite3")
	hitID := seedChunkIn(t, db, "peeq", "backend/internal/store/store.go", 0, 1, 20, "Open",
		"func Open(p string) error { return ZeroBlob(p) }")
	seedChunkIn(t, db, "go-sqlite3", "blob.go", 0, 40, 60, "ZeroBlob",
		"func ZeroBlob(p string) error { return nil }")
	seedSymbolIn(t, db, "go-sqlite3", "blob.go", "ZeroBlob", 40)
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'go-sqlite3'`); err != nil {
		t.Fatalf("park go-sqlite3: %v", err)
	}

	// When
	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})

	// Then
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if hasIn(got, "go-sqlite3", "blob.go") {
		t.Errorf("sources = %v, want the parked repository left out", repoPaths(got))
	}
}
