package indexer

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// TestSyncSpecs_snapshotRoundTripsAsAnEmptyCloneURL: no column and no migration.
// The empty clone_url IS the fact, and RepoState.Snapshot reads it back.
func TestSyncSpecs_snapshotRoundTripsAsAnEmptyCloneURL(t *testing.T) {
	// Given
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()

	// When
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "acme-core", Snapshot: true, Enabled: true, Project: "acme-core"},
		{Name: "shop", CloneURL: "/tmp/shop", Branch: "master", Enabled: true, Project: "shop"},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// Then
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All() err = %v", err)
	}
	got := map[string]bool{}
	for _, st := range all {
		got[st.Name] = st.Snapshot()
	}
	if !got["acme-core"] {
		t.Error("acme-core Snapshot() = false, want true")
	}
	if got["shop"] {
		t.Error("shop Snapshot() = true, want false")
	}
}

// TestSyncSpecs_aSnapshotStructureEditIsNotAReIndex: the same invariant the
// remote entries have. Editing part or description must leave last_sha and the
// branch alone, or every YAML tweak would re-index the drop.
func TestSyncSpecs_aSnapshotStructureEditIsNotAReIndex(t *testing.T) {
	// Given: an indexed snapshot
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "acme-core", Snapshot: true, Enabled: true, Project: "acme-core", Part: "backend"},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	if err := s.SetBranch(ctx, "acme-core", "snapshot"); err != nil {
		t.Fatalf("SetBranch() err = %v", err)
	}
	if err := s.MarkIndexed(ctx, "acme-core", "abc123", Counts{Files: 1, Chunks: 2}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}

	// When: only the structure changes
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "acme-core", Snapshot: true, Enabled: true, Project: "acme-core",
			Part: "service", Description: "Vendor drop, release 4.2."},
	}); err != nil {
		t.Fatalf("second SyncSpecs() err = %v", err)
	}

	// Then
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All() err = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(All()) = %d, want 1", len(all))
	}
	if all[0].LastSHA != "abc123" {
		t.Errorf("LastSHA = %q, want it untouched at abc123", all[0].LastSHA)
	}
	if all[0].Branch != "snapshot" {
		t.Errorf("Branch = %q, want it untouched at snapshot", all[0].Branch)
	}
	if all[0].Part != "service" {
		t.Errorf("Part = %q, want the edit applied", all[0].Part)
	}
}

// TestSyncSpecs_reportsWhetherAPurgedRepoWasASnapshot: the caller removes a
// clone's checkout but must NOT remove a snapshot's — the operator extracted
// that tree by hand, and rongo deleting it would destroy something it never
// created.
func TestSyncSpecs_reportsWhetherAPurgedRepoWasASnapshot(t *testing.T) {
	// Given
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "acme-core", Snapshot: true, Enabled: true, Project: "acme-core"},
		{Name: "shop", CloneURL: "/tmp/shop", Enabled: true, Project: "shop"},
		{Name: "keeper", CloneURL: "/tmp/keeper", Enabled: true, Project: "keeper"},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// When: both leave the list
	purged, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "keeper", CloneURL: "/tmp/keeper", Enabled: true, Project: "keeper"},
	})

	// Then
	if err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	if len(purged) != 2 {
		t.Fatalf("purged = %v, want two entries", purged)
	}
	snapshot := map[string]bool{}
	for _, p := range purged {
		snapshot[p.Name] = p.Snapshot
	}
	if !snapshot["acme-core"] {
		t.Error("acme-core purged as Snapshot = false, want true — its directory must be spared")
	}
	if snapshot["shop"] {
		t.Error("shop purged as Snapshot = true, want false — its checkout is rongo's to remove")
	}
}
