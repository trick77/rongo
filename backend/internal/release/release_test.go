package release

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/gitrepo/gittest"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store/storetest"
)

// fixture: a remote with three commits on main tagged 1.0.0 and v1.2.0, a
// checkout of it, and a repo_state row indexed at the SECOND commit, so the
// third is on the remote branch and not yet indexed.
func fixture(t *testing.T) (*Git, map[string]string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	src := gittest.Init(t)
	shas := map[string]string{}
	shas["c1"] = gittest.Commit(t, src, "a.txt", []byte("first\n"), "first")
	gittest.Run(t, src, "tag", "1.0.0")
	shas["c2"] = gittest.Commit(t, src, "b.txt", []byte("second\n"), "second")
	gittest.Run(t, src, "tag", "-a", "v1.2.0", "-m", "release")
	shas["c3"] = gittest.Commit(t, src, "c.txt", []byte("third\n"), "third")

	client := gitrepo.New(gitBin, t.TempDir())
	spec := repos.Spec{Name: "shop", CloneURL: src, Branch: "main", Enabled: true}
	if err := client.EnsureCloned(context.Background(), spec, ""); err != nil {
		t.Fatalf("EnsureCloned: %v", err)
	}

	db := storetest.Open(t, 4)
	seed(t, db, "shop", src, "main", shas["c2"])
	seed(t, db, "drop", "", "snapshot", shas["c1"])
	return New(client, db, 500), shas
}

func seed(t *testing.T, db *sql.DB, name, url, branch, sha string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch, last_sha, enabled) VALUES (?, ?, ?, ?, 1)`,
		name, url, branch, sha); err != nil {
		t.Fatal(err)
	}
}

func TestHead_readsTheIndexedCommitAndTheRemoteBranch(t *testing.T) {
	g, shas := fixture(t)
	h, err := g.Head(context.Background(), "shop")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if h.SHA != shas["c2"] || h.Remote != shas["c3"] || h.Branch != "main" || h.Snapshot {
		t.Errorf("head = %+v, want indexed c2, remote c3, main, no snapshot", h)
	}
}

func TestHead_aSnapshotHasNoRemote(t *testing.T) {
	g, shas := fixture(t)
	h, err := g.Head(context.Background(), "drop")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if !h.Snapshot || h.SHA != shas["c1"] || h.Remote != shas["c1"] {
		t.Errorf("head = %+v, want a snapshot at c1 on both sides", h)
	}
}

func TestHead_anUnknownRepositoryIsAnError(t *testing.T) {
	g, _ := fixture(t)
	if _, err := g.Head(context.Background(), "nope"); err == nil {
		t.Fatal("Head(nope) err = nil")
	}
}

func TestResolveTag_mapsTheMissOntoAsksSentinel(t *testing.T) {
	g, shas := fixture(t)
	ctx := context.Background()
	if sha, err := g.ResolveTag(ctx, "shop", "1.2.0"); err != nil || sha != shas["c2"] {
		t.Errorf("ResolveTag(1.2.0) = %s, %v; want c2", sha, err)
	}
	if _, err := g.ResolveTag(ctx, "shop", "9.9.9"); !errors.Is(err, ask.ErrVersionUnknown) {
		t.Errorf("ResolveTag(9.9.9) err = %v, want ErrVersionUnknown", err)
	}
}

func TestRange_listsNewestFirstAndIsAncestorAgrees(t *testing.T) {
	g, shas := fixture(t)
	ctx := context.Background()
	got, err := g.Range(ctx, "shop", shas["c1"], shas["c3"], 10)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(got) != 2 || got[0] != shas["c3"] || got[1] != shas["c2"] {
		t.Errorf("range = %v, want c3 then c2", got)
	}
	if ok, err := g.IsAncestor(ctx, "shop", shas["c1"], shas["c3"]); err != nil || !ok {
		t.Errorf("IsAncestor(c1, c3) = %v, %v", ok, err)
	}
	if ok, err := g.IsAncestor(ctx, "shop", shas["c3"], shas["c1"]); err != nil || ok {
		t.Errorf("IsAncestor(c3, c1) = %v, %v", ok, err)
	}
	if g.Depth() != 500 {
		t.Errorf("Depth = %d", g.Depth())
	}
}

// git's own failures carry ask's sentinel, so the release turn can tell a
// component whose git state it cannot determine from a database fault,
// which still fails the turn.
func TestGitFailuresCarryAsksSentinel(t *testing.T) {
	g, shas := fixture(t)
	ctx := context.Background()
	if _, err := g.ResolveTag(ctx, "ghost", "1.0.0"); !errors.Is(err, ask.ErrGitState) {
		t.Errorf("ResolveTag(ghost) err = %v, want ErrGitState", err)
	}
	if _, err := g.IsAncestor(ctx, "ghost", shas["c1"], shas["c2"]); !errors.Is(err, ask.ErrGitState) {
		t.Errorf("IsAncestor(ghost) err = %v, want ErrGitState", err)
	}
	if _, err := g.Range(ctx, "ghost", shas["c1"], shas["c2"], 10); !errors.Is(err, ask.ErrGitState) {
		t.Errorf("Range(ghost) err = %v, want ErrGitState", err)
	}
	if _, err := g.Head(ctx, "nope"); err == nil || errors.Is(err, ask.ErrGitState) {
		t.Errorf("Head(nope) err = %v, want a database error without ErrGitState", err)
	}
}
