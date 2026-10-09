package repostatus

import (
	"context"
	"database/sql"
	"testing"

	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store/storetest"
)

func seedIndexed(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO files (repo, path, sha) VALUES ('peeq', ?, 'sha')`, path)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	id, _ := res.LastInsertId()
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(
			`INSERT INTO chunks (file_id, ordinal, start_line, end_line, raw_text, content_hash)
			 VALUES (?, ?, 1, 2, 'r', ?)`, id, i, path+string(rune('a'+i))); err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
	}
}

func TestRepoStatus_countsModulesFromTheIndex(t *testing.T) {
	// Given: two packages large enough to stand on their own.
	db := storetest.Open(t, 4)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if _, err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true,
		Image: "registry.example.invalid/acme/peeq"}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	seedIndexed(t, db, "backend/internal/download/run.go")
	seedIndexed(t, db, "backend/internal/cookie/netscape.go")

	// When
	got, err := New(db, modules.Opts{MinChunks: 3, MaxChunks: 100}).RepoStatus(ctx)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}

	// Then
	if len(got) != 1 {
		t.Fatalf("got %d repositories, want 1", len(got))
	}
	if got[0].Modules != 2 {
		t.Errorf("Modules = %d, want 2", got[0].Modules)
	}
	if got[0].Name != "peeq" || !got[0].Enabled || got[0].Image != "registry.example.invalid/acme/peeq" {
		t.Errorf("status = %+v, want the active peeq row with its image", got[0])
	}
}

func TestRepoStatus_theClusteringConstantsActuallyReachTheCount(t *testing.T) {
	// Guard against a count that ignores Opts: with the same index, a stricter
	// fold must produce fewer modules. Without this, the page could report a
	// number derived from constants nobody set and it would look plausible.
	db := storetest.Open(t, 4)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if _, err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	seedIndexed(t, db, "backend/internal/download/run.go")
	seedIndexed(t, db, "backend/internal/cookie/netscape.go")

	loose, err := New(db, modules.Opts{MinChunks: 3, MaxChunks: 100}).RepoStatus(ctx)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}
	strict, err := New(db, modules.Opts{MinChunks: 50, MaxChunks: 100}).RepoStatus(ctx)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}

	if strict[0].Modules >= loose[0].Modules {
		t.Errorf("strict fold reported %d modules, loose %d — the constants are not reaching Cluster",
			strict[0].Modules, loose[0].Modules)
	}
}

func TestRepoStatus_aRepositoryThatLeftTheListIsGoneFromThePage(t *testing.T) {
	// Given: peeq indexed, then dropped from repos.yaml — which purges it.
	db := storetest.Open(t, 4)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if _, err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	seedIndexed(t, db, "backend/internal/download/run.go")
	if _, err := state.SyncSpecs(ctx, nil); err != nil {
		t.Fatalf("resync without peeq: %v", err)
	}

	// When
	got, err := New(db, modules.Opts{MinChunks: 3, MaxChunks: 100}).RepoStatus(ctx)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}

	// Then: the page shows what rongo holds, and it no longer holds peeq.
	if len(got) != 0 {
		t.Fatalf("got %+v, want nothing — the repository was purged, not deactivated", got)
	}
}

func TestRepoStatus_aParkedRepositoryKeepsItsIndexAndLeavesThePage(t *testing.T) {
	// Given: peeq indexed, then marked `enabled: false` in the YAML. That is a
	// repository being parked, not retired: its index and its row stay for
	// the citations already made, but it is not polled, not retrieved and
	// not on the Repos page.
	db := storetest.Open(t, 4)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if _, err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	seedIndexed(t, db, "backend/internal/download/run.go")
	if _, err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: false}}); err != nil {
		t.Fatalf("resync with peeq disabled: %v", err)
	}

	// When
	got, err := New(db, modules.Opts{MinChunks: 3, MaxChunks: 100}).RepoStatus(ctx)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Errorf("got %+v, want the parked repository off the page", got)
	}
	var files int
	if err := db.QueryRow(`SELECT COUNT(*) FROM files WHERE repo = 'peeq'`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Errorf("files = %d, want the index kept while parked", files)
	}
}

func TestRepoStatus_clustersOnceUntilTheIndexMoves(t *testing.T) {
	// The page is read on every turn and the clustering scans every file
	// and chunk, so the numbers are kept until the index state they were
	// read at changes.
	db := storetest.Open(t, 4)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if _, err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	seedIndexed(t, db, "backend/internal/download/run.go")
	s := New(db, modules.Opts{MinChunks: 3, MaxChunks: 100})

	for i := 0; i < 3; i++ {
		if _, err := s.RepoStatus(ctx); err != nil {
			t.Fatalf("RepoStatus %d: %v", i, err)
		}
	}
	if s.clusters != 1 {
		t.Errorf("clustered %d times over three reads of an unchanged index, want 1", s.clusters)
	}

	// The index moved: the totals changed.
	if err := state.SetCounts(ctx, "peeq", indexer.Counts{Files: 2, Chunks: 9}); err != nil {
		t.Fatalf("SetCounts: %v", err)
	}
	if _, err := s.RepoStatus(ctx); err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}
	if s.clusters != 2 {
		t.Errorf("clustered %d times after the index moved, want 2", s.clusters)
	}
}

func TestRepoStatus_carriesAQueuedReindex(t *testing.T) {
	db := storetest.Open(t, 4)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if _, err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := state.RequestReindex(ctx, "peeq"); err != nil {
		t.Fatal(err)
	}
	got, err := New(db, modules.Opts{MinChunks: 3, MaxChunks: 100}).RepoStatus(ctx)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}
	if len(got) != 1 || !got[0].ReindexQueued {
		t.Errorf("got %+v, want the queued request on the page", got)
	}
}
