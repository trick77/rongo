package indexer

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 1536); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSyncSpecs_insertsAndUpdates(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()

	// When
	err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true},
	})

	// Then
	if err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}
	if len(active) != 1 || active[0].Name != "peeq" {
		t.Fatalf("Active() = %+v, want one entry named peeq", active)
	}
	if active[0].Branch != "master" {
		t.Errorf("Branch = %q, want %q", active[0].Branch, "master")
	}
}

func TestSyncSpecs_deactivatesRatherThanDeletes(t *testing.T) {
	// Given: peeq was indexed, then dropped out of repos.yaml. Its index must
	// survive — a typo in the YAML must not destroy hours of indexing.
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true},
	}); err != nil {
		t.Fatalf("first SyncSpecs() err = %v", err)
	}
	if err := s.MarkIndexed(ctx, "peeq", "abc123", Counts{Files: 10, Chunks: 100}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}

	// When: the list no longer mentions peeq.
	if err := s.SyncSpecs(ctx, nil); err != nil {
		t.Fatalf("second SyncSpecs() err = %v", err)
	}

	// Then: it leaves the active set ...
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}
	if len(active) != 0 {
		t.Errorf("Active() = %+v, want empty after the repo left repos.yaml", active)
	}
	// ... but its row and its recorded work are still there.
	var enabled, chunkCount int
	if err := db.QueryRow(
		`SELECT enabled, chunk_count FROM repo_state WHERE name = ?`, "peeq",
	).Scan(&enabled, &chunkCount); err != nil {
		t.Fatalf("row missing entirely, want it deactivated not deleted: %v", err)
	}
	if enabled != 0 {
		t.Errorf("enabled = %d, want 0", enabled)
	}
	if chunkCount != 100 {
		t.Errorf("chunk_count = %d, want the recorded 100 to survive", chunkCount)
	}
}

func TestSyncSpecs_reenablesAReturningRepoWithoutLosingItsIndex(t *testing.T) {
	// Given: a repo removed from the list, then put back — the ordinary shape of
	// fixing a typo.
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	spec := repos.Spec{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true}
	if err := s.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	if err := s.MarkIndexed(ctx, "peeq", "abc123", Counts{Files: 10, Chunks: 100}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}
	if err := s.SyncSpecs(ctx, nil); err != nil {
		t.Fatalf("SyncSpecs(nil) err = %v", err)
	}

	// When
	if err := s.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("re-adding SyncSpecs() err = %v", err)
	}

	// Then: it is active again AND still knows the commit it had indexed, so
	// the next poll is an incremental diff rather than a full re-index.
	active, _ := s.Active(ctx)
	if len(active) != 1 {
		t.Fatalf("Active() = %+v, want the repo back", active)
	}
	if active[0].LastSHA != "abc123" {
		t.Errorf("LastSHA = %q, want %q — a re-added repo must not re-index from scratch",
			active[0].LastSHA, "abc123")
	}
}

func TestSyncSpecs_respectsAnExplicitlyDisabledEntry(t *testing.T) {
	// Given: the entry is present in the YAML but marked enabled: false.
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()

	// When
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "legacy", CloneURL: "/tmp/legacy", Enabled: false},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// Then
	active, _ := s.Active(ctx)
	if len(active) != 0 {
		t.Errorf("Active() = %+v, want empty for an entry with enabled: false", active)
	}
}

func TestMarkError_isVisibleAndClearedByASuccess(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "release-2024.3", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// When: the configured branch vanished upstream.
	if err := s.MarkError(ctx, "peeq", `branch "release-2024.3" not found`); err != nil {
		t.Fatalf("MarkError() err = %v", err)
	}

	// Then: it is visible on the state, not swallowed.
	active, _ := s.Active(ctx)
	if len(active) != 1 || active[0].LastError == "" {
		t.Fatalf("LastError is empty, want the branch failure surfaced")
	}

	// And a later success clears it, so a stale error cannot alarm forever.
	if err := s.MarkIndexed(ctx, "peeq", "def456", Counts{Files: 1, Chunks: 2}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}
	active, _ = s.Active(ctx)
	if active[0].LastError != "" {
		t.Errorf("LastError = %q, want it cleared after a successful run", active[0].LastError)
	}
}

func TestSetBranch_recordsTheResolvedBranch(t *testing.T) {
	// Given: an entry whose YAML omitted the branch, so it is empty until the
	// git layer resolves the remote's default.
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "go-sqlite3", CloneURL: "/tmp/x", Branch: "", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// When
	if err := s.SetBranch(ctx, "go-sqlite3", "main"); err != nil {
		t.Fatalf("SetBranch() err = %v", err)
	}

	// Then
	active, _ := s.Active(ctx)
	if active[0].Branch != "main" {
		t.Errorf("Branch = %q, want %q — never assume master", active[0].Branch, "main")
	}
}
