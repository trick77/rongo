package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/repos"
)

// snapshotFixture is one extracted drop under a temp repo root, recorded in a
// fresh database the way TestEvalIndex records repos.yaml.
func snapshotFixture(t *testing.T) (*gitrepo.Client, *indexer.StateStore, string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git: %v", err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "widget-drop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "main.txt"), "first\n")
	state := indexer.NewStateStore(queryCacheDB(t))
	spec := repos.Spec{Name: "widget-drop", Snapshot: true, Enabled: true}
	if _, err := state.SyncSpecs(context.Background(), []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs: %v", err)
	}
	return gitrepo.New(gitBin, root), state, dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func activeState(t *testing.T, state *indexer.StateStore) indexer.RepoState {
	t.Helper()
	active, err := state.Active(context.Background())
	if err != nil || len(active) != 1 {
		t.Fatalf("Active = %d rows, err %v; want 1", len(active), err)
	}
	return active[0]
}

func TestSnapshotHead_firstRunCommitsTheDropAndIndexesItWhole(t *testing.T) {
	// Given
	gitc, state, _ := snapshotFixture(t)
	st := activeState(t, state)

	// When
	sha, paths, unchanged, err := snapshotHead(context.Background(), gitc, state, &st)

	// Then
	if err != nil {
		t.Fatalf("snapshotHead: %v", err)
	}
	if sha == "" || paths != nil || unchanged {
		t.Errorf("sha %q paths %v unchanged %v, want a sha, a full index, changed", sha, paths, unchanged)
	}
	if st.Branch != gitrepo.SnapshotBranch || activeState(t, state).Branch != gitrepo.SnapshotBranch {
		t.Errorf("branch = %q, recorded %q; want %q", st.Branch, activeState(t, state).Branch, gitrepo.SnapshotBranch)
	}
}

func TestSnapshotHead_untouchedDropIsUnchanged(t *testing.T) {
	// Given: indexed once.
	ctx := context.Background()
	gitc, state, _ := snapshotFixture(t)
	st := activeState(t, state)
	sha, _, _, err := snapshotHead(ctx, gitc, state, &st)
	if err != nil {
		t.Fatalf("first snapshotHead: %v", err)
	}
	if err := state.MarkIndexed(ctx, st.Name, sha, indexer.Counts{}); err != nil {
		t.Fatal(err)
	}

	// When
	st = activeState(t, state)
	again, _, unchanged, err := snapshotHead(ctx, gitc, state, &st)

	// Then
	if err != nil || !unchanged || again != sha {
		t.Errorf("second run = %q unchanged %v err %v; want %q unchanged", again, unchanged, err, sha)
	}
}

func TestSnapshotHead_editedDropIndexesOnlyTheChangedPaths(t *testing.T) {
	// Given: indexed once, then one file edited.
	ctx := context.Background()
	gitc, state, dir := snapshotFixture(t)
	st := activeState(t, state)
	sha, _, _, err := snapshotHead(ctx, gitc, state, &st)
	if err != nil {
		t.Fatalf("first snapshotHead: %v", err)
	}
	if err := state.MarkIndexed(ctx, st.Name, sha, indexer.Counts{}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "main.txt"), "second\n")

	// When
	st = activeState(t, state)
	next, paths, unchanged, err := snapshotHead(ctx, gitc, state, &st)

	// Then
	if err != nil || unchanged || next == sha {
		t.Fatalf("after an edit = %q unchanged %v err %v; want a new sha", next, unchanged, err)
	}
	if len(paths) != 1 || paths[0] != "main.txt" {
		t.Errorf("paths = %v, want [main.txt]", paths)
	}
}

func TestSnapshotHead_replacedDropResetsAndIndexesItWhole(t *testing.T) {
	// Given: indexed once, then the drop extracted afresh, which leaves a new
	// object store without the recorded commit.
	ctx := context.Background()
	gitc, state, dir := snapshotFixture(t)
	st := activeState(t, state)
	sha, _, _, err := snapshotHead(ctx, gitc, state, &st)
	if err != nil {
		t.Fatalf("first snapshotHead: %v", err)
	}
	if err := state.MarkIndexed(ctx, st.Name, sha, indexer.Counts{Files: 1, Chunks: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "main.txt"), "third\n")

	// When
	st = activeState(t, state)
	_, paths, unchanged, err := snapshotHead(ctx, gitc, state, &st)

	// Then
	if err != nil || unchanged || paths != nil {
		t.Fatalf("replaced drop: paths %v unchanged %v err %v; want a full index", paths, unchanged, err)
	}
	if st.LastSHA != "" || activeState(t, state).LastSHA != "" {
		t.Errorf("last_sha kept across a replaced drop; want it reset")
	}
}

func TestSnapshotHead_missingDropIsAnError(t *testing.T) {
	// Given
	gitc, state, dir := snapshotFixture(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	st := activeState(t, state)

	// When
	_, _, _, err := snapshotHead(context.Background(), gitc, state, &st)

	// Then
	if err == nil {
		t.Error("a missing drop must fail, never index as empty")
	}
}
