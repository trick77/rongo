package gitrepo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// TestEntries_carryTheBlobSize: the indexer refuses a file over its ceiling
// before reading it, so both listings say how big each blob is. A submodule
// pointer names a commit, not a blob, and has no size to report.
func TestEntries_carryTheBlobSize(t *testing.T) {
	src := fixtureRepo(t)
	c := newClient(t)
	ctx := context.Background()
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	first, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}
	writeAndCommit(t, src, "big.txt", strings.Repeat("x", 5000), "big")
	gitRun(t, src, "update-index", "--add", "--cacheinfo", "160000,"+first+",sub")
	gitRun(t, src, "commit", "-qm", "submodule")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}
	second, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}

	listed, err := c.ListEntries(ctx, spec, second)
	if err != nil {
		t.Fatalf("ListEntries() err = %v", err)
	}
	changed, err := c.ChangedEntries(ctx, spec, first, second)
	if err != nil {
		t.Fatalf("ChangedEntries() err = %v", err)
	}
	for name, entries := range map[string][]Change{"ListEntries": listed, "ChangedEntries": changed} {
		sizes := map[string]int64{}
		for _, e := range entries {
			sizes[e.Path] = e.Size
		}
		if sizes["big.txt"] != 5000 {
			t.Errorf("%s: big.txt size = %d, want 5000", name, sizes["big.txt"])
		}
		if s, ok := sizes["sub"]; !ok || s != 0 {
			t.Errorf("%s: sub size = %d (listed %v), want 0", name, s, ok)
		}
	}
}

// TestReader_aLimitedReadRefusesABigBlobAndStaysInStep: past the limit the
// blob is skipped on the stream, never held, and the next read on the same
// process returns its own file.
func TestReader_aLimitedReadRefusesABigBlobAndStaysInStep(t *testing.T) {
	src := fixtureRepo(t)
	writeAndCommit(t, src, "big.txt", strings.Repeat("x", 5000), "big")
	c := newClient(t)
	ctx := context.Background()
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	sha, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}
	r, err := c.NewReader(ctx, spec)
	if err != nil {
		t.Fatalf("NewReader() err = %v", err)
	}
	defer func() { _ = r.Close() }()

	_, err = r.ReadFileLimited(ctx, spec, sha, "big.txt", 4096)
	var tl *TooLargeError
	if !errors.As(err, &tl) || tl.Size != 5000 {
		t.Fatalf("ReadFileLimited(big.txt) err = %v, want TooLargeError of 5000", err)
	}
	if body, err := r.ReadFileLimited(ctx, spec, sha, "a.txt", 4096); err != nil || string(body) != "first\n" {
		t.Errorf("next read = %q, %v; want a.txt's bytes", body, err)
	}
	if body, err := r.ReadFile(ctx, spec, sha, "big.txt"); err != nil || len(body) != 5000 {
		t.Errorf("unlimited read = %d bytes, %v; want 5000", len(body), err)
	}
}
