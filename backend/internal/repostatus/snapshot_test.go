package repostatus

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/repos"
)

// TestRepoStatus_saysWhichRowsAreSnapshots: a snapshot's last_sha never moves on
// its own, so without the word a correct one-off index reads exactly like a
// poller that quietly stopped.
func TestRepoStatus_saysWhichRowsAreSnapshots(t *testing.T) {
	// Given
	db := statusDB(t)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if _, err := state.SyncSpecs(ctx, []repos.Spec{
		{Name: "acme-core", Snapshot: true, Enabled: true, Project: "acme-core"},
		{Name: "peeq", CloneURL: "file:///x", Enabled: true, Project: "peeq"},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// When
	got, err := New(db, modules.Opts{MinChunks: 3, MaxChunks: 100}).RepoStatus(ctx)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}

	// Then
	if len(got) != 2 {
		t.Fatalf("got %d repositories, want 2", len(got))
	}
	snapshot := map[string]bool{}
	for _, st := range got {
		snapshot[st.Name] = st.Snapshot
	}
	if !snapshot["acme-core"] {
		t.Error("acme-core Snapshot = false, want true")
	}
	if snapshot["peeq"] {
		t.Error("peeq Snapshot = true, want false")
	}
}
