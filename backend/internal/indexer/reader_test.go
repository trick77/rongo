package indexer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
)

// countingGit is the real git with its reads counted: how many batch readers
// a run opened, and how many files it read a process at a time.
type countingGit struct {
	*gitrepo.Client
	readers atomic.Int32
	singles atomic.Int32
	// refuse makes NewReader fail, as a git that will not start does.
	refuse bool
}

func (g *countingGit) NewReader(ctx context.Context, spec repos.Spec) (*gitrepo.Reader, error) {
	g.readers.Add(1)
	if g.refuse {
		return nil, errors.New("fork/exec git: resource temporarily unavailable")
	}
	return g.Client.NewReader(ctx, spec)
}

func (g *countingGit) ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error) {
	g.singles.Add(1)
	return g.Client.ReadFile(ctx, spec, sha, path)
}

func TestIndexRepo_readsAWholeRunThroughOneGitProcess(t *testing.T) {
	// Given
	h := newHarness(t, nil)
	git := &countingGit{Client: h.gitc}
	h.ix.git = git

	// When the repository is indexed in full
	counts, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), h.head(t), nil)

	// Then every file came through the one reader, manifests included
	if err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}
	if counts.Files == 0 {
		t.Fatal("nothing was indexed")
	}
	if git.readers.Load() != 1 || git.singles.Load() != 0 {
		t.Errorf("opened %d readers and read %d files a process at a time, want 1 and 0",
			git.readers.Load(), git.singles.Load())
	}
}

func TestIndexRepo_indexesTheSameWhenTheBatchReaderWillNotStart(t *testing.T) {
	// Given one run through the batch reader, as the reference
	h := newHarness(t, nil)
	st, sha := h.stateOf(t), h.head(t)
	want, err := h.ix.IndexRepo(context.Background(), st, sha, nil)
	if err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}
	wantChunks := countOf(t, h.db, `SELECT COUNT(*) FROM chunks`)
	wantHashes := countOf(t, h.db, `SELECT COUNT(DISTINCT content_hash) FROM chunks`)

	// When the reader cannot be started and the run is repeated
	git := &countingGit{Client: h.gitc, refuse: true}
	h.ix.git = git
	got, err := h.ix.IndexRepo(context.Background(), st, sha, nil)

	// Then the run reads a file at a time and ends with the same index
	if err != nil {
		t.Fatalf("IndexRepo() without the reader err = %v", err)
	}
	if git.singles.Load() == 0 {
		t.Error("no file was read the slow way; the fallback did not run")
	}
	if got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM chunks`); n != wantChunks {
		t.Errorf("chunks = %d, want %d", n, wantChunks)
	}
	if n := countOf(t, h.db, `SELECT COUNT(DISTINCT content_hash) FROM chunks`); n != wantHashes {
		t.Errorf("distinct chunk contents = %d, want %d", n, wantHashes)
	}
}
