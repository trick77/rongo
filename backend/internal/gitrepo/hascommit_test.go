package gitrepo

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo/gittest"
	"github.com/trick77/rongo/internal/repos"
)

// TestHasCommit_tellsAMissingCommitFromAGitThatFailed: only git's "no such
// object" is absence. Anything else read as absence would reset a healthy
// clone's whole index over one failed process.
func TestHasCommit_tellsAMissingCommitFromAGitThatFailed(t *testing.T) {
	src := gittest.Fixture(t)
	c := newClient(t)
	ctx := context.Background()
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	head, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}

	if ok, err := c.HasCommit(ctx, spec, head); !ok || err != nil {
		t.Errorf("HasCommit(head) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := c.HasCommit(ctx, spec, strings.Repeat("ab", 20)); ok || err != nil {
		t.Errorf("HasCommit(absent) = %v, %v; want false, nil", ok, err)
	}
	gone := repos.Spec{Name: "never-cloned", CloneURL: src, Branch: "main", Enabled: true}
	if ok, err := c.HasCommit(ctx, gone, head); ok || err == nil {
		t.Errorf("HasCommit(no checkout) = %v, %v; want the failure", ok, err)
	}
}
