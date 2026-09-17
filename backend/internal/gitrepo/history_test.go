package gitrepo

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

func TestLog_listsCommitsNewestFirstWithPaths(t *testing.T) {
	// Given: a fixture with three commits, the last one carrying a body.
	src := fixtureRepo(t)
	writeAndCommit(t, src, "b.txt", "b\n", "second: add b")
	writeAndCommit(t, src, "c.txt", "c\n", "third\n\nA body line.\nAnother.")
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	head, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}

	// When
	got, err := c.Log(ctx, spec, "", head, 100)

	// Then
	if err != nil {
		t.Fatalf("Log() err = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Log() = %d commits, want 3: %+v", len(got), got)
	}
	if got[0].SHA != head {
		t.Errorf("first commit = %s, want head %s", got[0].SHA, head)
	}
	if got[0].Subject != "third" || got[0].Body != "A body line.\nAnother." {
		t.Errorf("subject/body = %q / %q", got[0].Subject, got[0].Body)
	}
	if len(got[0].Paths) != 1 || got[0].Paths[0] != "c.txt" {
		t.Errorf("paths = %v, want [c.txt]", got[0].Paths)
	}
	if got[0].Author != "t" {
		t.Errorf("author = %q, want t", got[0].Author)
	}
	if !strings.HasPrefix(got[0].CommittedAt, "20") || !strings.Contains(got[0].CommittedAt, "T") {
		t.Errorf("committed_at = %q, want ISO 8601", got[0].CommittedAt)
	}
	if got[2].Subject != "first" || len(got[2].Paths) != 1 || got[2].Paths[0] != "a.txt" {
		t.Errorf("root commit = %+v", got[2])
	}
}

func TestLog_fromToListsOnlyTheNewSide_andLimitCaps(t *testing.T) {
	// Given
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	before, _ := c.HeadSHA(ctx, spec, "main")
	writeAndCommit(t, src, "b.txt", "b\n", "second")
	writeAndCommit(t, src, "c.txt", "c\n", "third")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}
	after, _ := c.HeadSHA(ctx, spec, "main")

	// When
	got, err := c.Log(ctx, spec, before, after, 100)
	if err != nil {
		t.Fatalf("Log() err = %v", err)
	}
	// Then: the two new commits, not the root.
	if len(got) != 2 || got[0].Subject != "third" || got[1].Subject != "second" {
		t.Errorf("Log(from, to) = %+v, want [third second]", got)
	}

	// When: a limit smaller than the history.
	capped, err := c.Log(ctx, spec, "", after, 1)
	if err != nil {
		t.Fatalf("Log() err = %v", err)
	}
	// Then
	if len(capped) != 1 || capped[0].Subject != "third" {
		t.Errorf("Log(limit 1) = %+v, want [third]", capped)
	}
}

func TestLog_firstParentCollapsesAMergeToOneEntry(t *testing.T) {
	// Given: main with a side branch of two commits merged in with a merge
	// commit, the way a forge's merge button leaves history.
	src := fixtureRepo(t)
	gitRun(t, src, "checkout", "-qb", "side")
	writeAndCommit(t, src, "s1.txt", "1\n", "side one")
	writeAndCommit(t, src, "s2.txt", "2\n", "side two")
	gitRun(t, src, "checkout", "-q", "main")
	gitRun(t, src, "merge", "-q", "--no-ff", "-m", "Merge pull request #7 from side", "side")
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	head, _ := c.HeadSHA(ctx, spec, "main")

	// When
	got, err := c.Log(ctx, spec, "", head, 100)

	// Then: the merge and the root, never the side commits; the merge's paths
	// are what it brought onto main.
	if err != nil {
		t.Fatalf("Log() err = %v", err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0].Subject, "Merge pull request #7") {
		t.Fatalf("Log() = %+v, want [merge, first]", got)
	}
	if strings.Join(got[0].Paths, ",") != "s1.txt,s2.txt" {
		t.Errorf("merge paths = %v, want [s1.txt s2.txt]", got[0].Paths)
	}
}

func TestShow_reportsOneCommitWithCounts(t *testing.T) {
	// Given
	src := fixtureRepo(t)
	writeAndCommit(t, src, "b.txt", "one\ntwo\n", "second\n\nWhy it changed.")
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	head, _ := c.HeadSHA(ctx, spec, "main")

	// When
	got, err := c.Show(ctx, spec, head)

	// Then
	if err != nil {
		t.Fatalf("Show() err = %v", err)
	}
	if got.SHA != head || got.Subject != "second" || got.Body != "Why it changed." {
		t.Errorf("Show() = %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "b.txt" || got.Files[0].Added != 2 || got.Files[0].Deleted != 0 {
		t.Errorf("files = %+v, want b.txt +2 -0", got.Files)
	}

	// When: a commit the store does not hold.
	_, err = c.Show(ctx, spec, "0000000000000000000000000000000000000000")
	// Then
	if err == nil {
		t.Error("Show() of an unknown commit did not fail")
	}
}
