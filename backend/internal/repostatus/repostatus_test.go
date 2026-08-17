package repostatus

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store"
)

func statusDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedIndexed(t *testing.T, db *sql.DB, repo, path string, chunks int) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO files (repo, path, sha) VALUES (?, ?, 'sha')`, repo, path)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	id, _ := res.LastInsertId()
	for i := 0; i < chunks; i++ {
		if _, err := db.Exec(
			`INSERT INTO chunks (file_id, ordinal, start_line, end_line, text, raw_text, content_hash)
			 VALUES (?, ?, 1, 2, 'e', 'r', ?)`, id, i, path+string(rune('a'+i))); err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
	}
}

func TestRepoStatus_countsModulesFromTheIndex(t *testing.T) {
	// Given: two packages large enough to stand on their own.
	db := statusDB(t)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	seedIndexed(t, db, "peeq", "backend/internal/download/run.go", 5)
	seedIndexed(t, db, "peeq", "backend/internal/cookie/netscape.go", 5)

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
	if got[0].Name != "peeq" || !got[0].Enabled {
		t.Errorf("status = %+v, want the active peeq row", got[0])
	}
}

func TestRepoStatus_theClusteringConstantsActuallyReachTheCount(t *testing.T) {
	// Guard against a count that ignores Opts: with the same index, a stricter
	// fold must produce fewer modules. Without this, the page could report a
	// number derived from constants nobody set and it would look plausible.
	db := statusDB(t)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	seedIndexed(t, db, "peeq", "backend/internal/download/run.go", 5)
	seedIndexed(t, db, "peeq", "backend/internal/cookie/netscape.go", 5)

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

func TestRepoStatus_deactivatedRepositoriesKeepTheirIndexAndTheirRow(t *testing.T) {
	// Given: peeq indexed, then dropped from repos.yaml.
	db := statusDB(t)
	state := indexer.NewStateStore(db)
	ctx := context.Background()
	if err := state.SyncSpecs(ctx, []repos.Spec{{Name: "peeq", CloneURL: "file:///x", Enabled: true}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	seedIndexed(t, db, "peeq", "backend/internal/download/run.go", 5)
	if err := state.SyncSpecs(ctx, nil); err != nil {
		t.Fatalf("resync without peeq: %v", err)
	}

	// When
	got, err := New(db, modules.Opts{MinChunks: 3, MaxChunks: 100}).RepoStatus(ctx)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}

	// Then
	if len(got) != 1 {
		t.Fatalf("got %d repositories, want the deactivated one kept", len(got))
	}
	if got[0].Enabled {
		t.Error("Enabled = true, want false")
	}
	if got[0].Modules != 1 {
		t.Errorf("Modules = %d, want 1 — the index survives deactivation", got[0].Modules)
	}
}
