package modules

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

const testDim = 4

// clusterDB opens a migrated database with two repositories, because a module
// must never reach across a repo boundary.
func clusterDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, testDim); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, name := range []string{"peeq", "loom"} {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url) VALUES (?, 'file:///x')`, name); err != nil {
			t.Fatalf("seed repo_state %s: %v", name, err)
		}
	}
	return db
}

// seedFile inserts one file with n chunks. skip marks it as deliberately not
// indexed, which must keep it out of every module.
func seedFile(t *testing.T, db *sql.DB, repo, path string, chunks int, skip string) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO files (repo, path, sha, skip_reason) VALUES (?, ?, 'deadbeef', ?)`, repo, path, skip)
	if err != nil {
		t.Fatalf("seed file %s: %v", path, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("file id: %v", err)
	}
	for i := 0; i < chunks; i++ {
		if _, err := db.Exec(
			`INSERT INTO chunks (file_id, ordinal, start_line, end_line, text, raw_text, content_hash)
			 VALUES (?, ?, 1, 2, 'enriched', 'raw', ?)`,
			id, i, path+":"+string(rune('a'+i)),
		); err != nil {
			t.Fatalf("seed chunk %s#%d: %v", path, i, err)
		}
	}
}

func keys(mods []Module) []string {
	out := make([]string, len(mods))
	for i, m := range mods {
		out[i] = m.Key
	}
	return out
}

func find(t *testing.T, mods []Module, key string) Module {
	t.Helper()
	for _, m := range mods {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("no module %q in %v", key, keys(mods))
	return Module{}
}

func TestCluster_directoryWithEnoughChunksBecomesItsOwnModule(t *testing.T) {
	// Given
	db := clusterDB(t)
	seedFile(t, db, "peeq", "backend/internal/download/freebytes.go", 3, "")
	seedFile(t, db, "peeq", "backend/internal/download/run.go", 2, "")
	seedFile(t, db, "peeq", "backend/internal/cookie/netscape.go", 4, "")

	// When
	mods, err := Cluster(context.Background(), db, "peeq", Opts{MinChunks: 3, MaxChunks: 100})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	// Then
	want := []string{"backend/internal/cookie", "backend/internal/download"}
	if got := keys(mods); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if m := find(t, mods, "backend/internal/download"); m.ChunkCount != 5 {
		t.Errorf("download ChunkCount = %d, want 5", m.ChunkCount)
	}
	if m := find(t, mods, "backend/internal/download"); len(m.Paths) != 2 {
		t.Errorf("download Paths = %v, want 2 entries", m.Paths)
	}
}

func TestCluster_directoryBelowMinChunksIsFoldedIntoItsParent(t *testing.T) {
	// Given: version/ is a stub that must not become a module of its own.
	db := clusterDB(t)
	seedFile(t, db, "peeq", "backend/internal/download/run.go", 5, "")
	seedFile(t, db, "peeq", "backend/internal/version/version.go", 1, "")

	// When
	mods, err := Cluster(context.Background(), db, "peeq", Opts{MinChunks: 3, MaxChunks: 100})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	// Then: version's single chunk rides along upwards. Folding runs to a fixed
	// point rather than one step: stopping at the first parent would leave a
	// chain of near-empty modules ("backend/internal" holding one chunk), which
	// is the very thing the rule exists to prevent. What is too small to stand
	// on its own ends in the repository-root catch-all.
	if got := keys(mods); len(got) != 2 {
		t.Fatalf("keys = %v, want download plus the root catch-all", got)
	}
	root := find(t, mods, ".")
	if root.ChunkCount != 1 {
		t.Errorf("root ChunkCount = %d, want 1", root.ChunkCount)
	}
	if len(root.Paths) != 1 || root.Paths[0] != "backend/internal/version/version.go" {
		t.Errorf("root Paths = %v, want the folded version file", root.Paths)
	}
}

func TestCluster_skippedFilesAreNotPartOfAnyModule(t *testing.T) {
	// Given: a vendored directory that the Selector marked as skipped. Counting
	// it would invent a module nobody can be answered from.
	db := clusterDB(t)
	seedFile(t, db, "peeq", "backend/internal/download/run.go", 4, "")
	seedFile(t, db, "peeq", "vendor/lib/big.go", 0, "vendored")

	// When
	mods, err := Cluster(context.Background(), db, "peeq", Opts{MinChunks: 1, MaxChunks: 100})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	// Then
	if got := keys(mods); len(got) != 1 || got[0] != "backend/internal/download" {
		t.Fatalf("keys = %v, want only the indexed directory", got)
	}
}

func TestCluster_otherRepositoriesAreNotMixedIn(t *testing.T) {
	// Given: the same path in two repositories.
	db := clusterDB(t)
	seedFile(t, db, "peeq", "backend/internal/chat/store.go", 4, "")
	seedFile(t, db, "loom", "backend/internal/chat/store.go", 4, "")

	// When
	mods, err := Cluster(context.Background(), db, "peeq", Opts{MinChunks: 1, MaxChunks: 100})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	// Then
	if len(mods) != 1 {
		t.Fatalf("modules = %v, want exactly peeq's", keys(mods))
	}
	if m := mods[0]; m.ChunkCount != 4 || m.Repo != "peeq" {
		t.Errorf("module = %+v, want 4 chunks in peeq", m)
	}
}

func TestCluster_oversizedParentGivesItsFoldedChildrenBackTheirIdentity(t *testing.T) {
	// Given: three packages, each too small on its own, whose combined weight
	// would make their parent a single unroutable blob.
	db := clusterDB(t)
	seedFile(t, db, "peeq", "backend/internal/a/x.go", 4, "")
	seedFile(t, db, "peeq", "backend/internal/b/y.go", 4, "")
	seedFile(t, db, "peeq", "backend/internal/c/z.go", 4, "")

	// When
	mods, err := Cluster(context.Background(), db, "peeq", Opts{MinChunks: 5, MaxChunks: 10})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	// Then: the split wins over the fold. Without it this is one module of 12
	// chunks spanning three unrelated subjects.
	want := []string{"backend/internal/a", "backend/internal/b", "backend/internal/c"}
	got := keys(mods)
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
	for _, m := range mods {
		if m.ChunkCount != 4 {
			t.Errorf("%s ChunkCount = %d, want 4", m.Key, m.ChunkCount)
		}
	}
}

func TestCluster_flatDirectoryOverMaxChunksStaysWholeAndIsFlagged(t *testing.T) {
	// Given: ui/src/components in peeq — many files, one level, nothing to
	// split at. The honest outcome is one big module that says so, not a
	// pretend split along an arbitrary boundary.
	db := clusterDB(t)
	seedFile(t, db, "peeq", "ui/src/components/Player.tsx", 6, "")
	seedFile(t, db, "peeq", "ui/src/components/Sidebar.tsx", 6, "")

	// When
	mods, err := Cluster(context.Background(), db, "peeq", Opts{MinChunks: 1, MaxChunks: 10})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	// Then
	if len(mods) != 1 {
		t.Fatalf("modules = %v, want one", keys(mods))
	}
	m := mods[0]
	if m.Key != "ui/src/components" || m.ChunkCount != 12 {
		t.Fatalf("module = %+v, want the whole flat directory", m)
	}
	if !m.Oversized {
		t.Error("Oversized = false, want true — an unsplittable oversized module must be visible, not silent")
	}
}
