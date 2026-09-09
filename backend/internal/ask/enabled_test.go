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

// TestGather_aParkedRepositoryDoesNotCostALiveHop: filtering only the final
// join is not enough. maxDefiners (4) is counted over the whole symbols table,
// so a parked repository defining a name in several files pushes it past the
// threshold, drops it from `selective`, and a LIVE repository loses a hop it
// should have made. Excluded code must influence nothing, not merely stay out
// of the result.
func TestGather_aParkedRepositoryDoesNotCostALiveHop(t *testing.T) {
	// Given: peeq references Helper, which loom defines once — and a parked
	// repository defines it in five more files, over the budget on its own.
	db := gatherDB(t)
	seedRepo(t, db, "loom")
	seedRepo(t, db, "vendor-dump")
	hitID := seedChunkIn(t, db, "peeq", "backend/internal/store/store.go", 0, 1, 20, "Open",
		"func Open(p string) error { return Helper(p) }")
	seedChunkIn(t, db, "loom", "helper.go", 0, 40, 60, "Helper",
		"func Helper(p string) error { return nil }")
	seedSymbolIn(t, db, "loom", "helper.go", "Helper", 40)
	for _, path := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		seedChunkIn(t, db, "vendor-dump", path, 0, 1, 10, "Helper", "func Helper() {}")
		seedSymbolIn(t, db, "vendor-dump", path, "Helper", 1)
	}
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'vendor-dump'`); err != nil {
		t.Fatalf("park vendor-dump: %v", err)
	}

	// When
	got, err := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).
		Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hitID)})

	// Then
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !hasIn(got, "loom", "helper.go") {
		t.Errorf("sources = %v, want loom's definition — the parked repository ate the budget",
			repoPaths(got))
	}
	if hasIn(got, "vendor-dump", "a.go") {
		t.Errorf("sources = %v, want the parked repository left out entirely", repoPaths(got))
	}
}
