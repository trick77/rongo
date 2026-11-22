package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo/gittest"
	"github.com/trick77/rongo/internal/repos"
)

func TestRemoteBranches_listsEveryFetchedBranchButTheHEADAlias(t *testing.T) {
	// Given: a remote with month branches; the clone's origin/HEAD is the
	// symbolic ref to main, which names no branch of its own.
	src := gittest.Fixture(t)
	gittest.Run(t, src, "branch", "rel/2026-10.1")
	c := newClient(t)
	spec := repos.Spec{Name: "letters", CloneURL: src, Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	gittest.Run(t, src, "branch", "rel/2026-11.1")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}

	// When
	got, err := c.RemoteBranches(ctx, spec)

	// Then
	if err != nil {
		t.Fatalf("RemoteBranches() err = %v", err)
	}
	want := []string{"main", "rel/2026-10.1", "rel/2026-11.1"}
	if !slices.Equal(got, want) {
		t.Errorf("RemoteBranches() = %v, want %v", got, want)
	}
}

// TestRemoteBranches_aRemoteBranchNamedHEAD: `git branch HEAD` is refused,
// but a ref written by hand is a branch the remote serves. While the
// clone's origin/HEAD is the symbolic ref, fetch leaves it alone and the
// default branch stays where the remote has it; once that ref is gone, the
// fetched branch takes its place and is listed under its own name.
func TestRemoteBranches_aRemoteBranchNamedHEAD(t *testing.T) {
	// Given
	src := gittest.Fixture(t)
	first := gittest.Run(t, src, "rev-parse", "HEAD")
	head := gittest.Commit(t, src, "b.txt", []byte("second\n"), "second")
	gittest.Run(t, src, "update-ref", "refs/heads/HEAD", first)
	gittest.Run(t, src, "branch", "rel/2026-11.1")
	c := newClient(t)
	spec := repos.Spec{Name: "letters", CloneURL: src, Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}

	// When
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}
	got, err := c.RemoteBranches(ctx, spec)

	// Then: the default branch is untouched and resolves as before.
	if err != nil {
		t.Fatalf("RemoteBranches() err = %v", err)
	}
	if main, err := c.HeadSHA(ctx, spec, "main"); err != nil || main != head {
		t.Errorf("HeadSHA(main) = %q, %v; want %q: a branch named HEAD must not move the default", main, err, head)
	}
	if def, err := c.DefaultBranch(ctx, spec, ""); err != nil || def != "main" {
		t.Errorf("DefaultBranch() = %q, %v; want main", def, err)
	}
	for _, b := range got {
		if b != "main" && b != "rel/2026-11.1" && b != "HEAD" {
			t.Errorf("RemoteBranches() = %v, want main, rel/2026-11.1 and at most a real HEAD", got)
		}
	}
	if !slices.Contains(got, "rel/2026-11.1") || !slices.Contains(got, "main") {
		t.Errorf("RemoteBranches() = %v, want main and rel/2026-11.1", got)
	}

	// When: the alias is gone, so the next fetch mirrors the branch.
	gittest.Run(t, c.Dir(spec), "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() without the alias err = %v", err)
	}
	got, err = c.RemoteBranches(ctx, spec)

	// Then: a real ref, listed by its name; main still where it was.
	if err != nil {
		t.Fatalf("RemoteBranches() err = %v", err)
	}
	if want := []string{"HEAD", "main", "rel/2026-11.1"}; !slices.Equal(got, want) {
		t.Errorf("RemoteBranches() = %v, want %v", got, want)
	}
	if main, err := c.HeadSHA(ctx, spec, "main"); err != nil || main != head {
		t.Errorf("HeadSHA(main) = %q, %v; want %q", main, err, head)
	}
}

func TestRemoteBranches_reportsAMissingCheckout(t *testing.T) {
	c := newClient(t)
	spec := repos.Spec{Name: "letters", CloneURL: "/nowhere", Enabled: true}
	if err := os.MkdirAll(filepath.Join(c.root, "letters"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoteBranches(context.Background(), spec); err == nil {
		t.Error("RemoteBranches() err = nil, want a checkout that is not one reported")
	}
}
