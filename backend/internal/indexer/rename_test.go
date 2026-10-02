package indexer

import (
	"context"
	"testing"
)

func TestARenamedFileLeavesTheIndexUnderItsOldPath(t *testing.T) {
	// Given a repository indexed in full
	h := newHarness(t, nil)
	ctx := context.Background()
	st := h.stateOf(t)
	first := h.head(t)
	if _, err := h.ix.IndexRepo(ctx, st, first, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM files WHERE path = 'README.md'`); n != 1 {
		t.Fatalf("README.md has %d rows after the full run, want 1", n)
	}

	// When a file is renamed and the next poll indexes what the diff names
	git(t, h.src, "mv", "README.md", "GUIDE.md")
	git(t, h.src, "commit", "-qm", "rename the readme")
	next := h.head(t)
	paths, err := h.gitc.ChangedPaths(ctx, h.spec, first, next)
	if err != nil {
		t.Fatalf("ChangedPaths() err = %v", err)
	}
	st.LastSHA = first
	if _, err := h.ix.IndexRepo(ctx, st, next, paths); err != nil {
		t.Fatalf("incremental IndexRepo() err = %v", err)
	}

	// Then the old path is gone with its chunks: left behind, it went on
	// being retrieved and cited at a path the branch no longer has, and no
	// later diff would ever name it again
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM files WHERE path = 'README.md'`); n != 0 {
		t.Errorf("README.md still has %d rows after it was renamed away", n)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id WHERE f.path = 'GUIDE.md'`); n == 0 {
		t.Error("GUIDE.md has no chunks; the new path was not indexed")
	}
}
