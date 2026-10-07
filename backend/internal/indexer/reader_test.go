package indexer

import (
	"context"
	"errors"
	"fmt"
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

func TestARunOutlivesItsBatchReader(t *testing.T) {
	// Given a batch reader that dies on its third read, as a git that was
	// killed under the run does
	ix := New(Deps{})
	var batched, singles []string
	batch := func(_ context.Context, _ repos.Spec, _, path string, _ int64) ([]byte, error) {
		batched = append(batched, path)
		if len(batched) >= 3 {
			return nil, fmt.Errorf("read %s: %w: EOF", path, gitrepo.ErrReaderBroken)
		}
		if path == "gone.go" {
			return nil, errors.New("no such object")
		}
		return []byte("batch:" + path), nil
	}
	single := func(_ context.Context, _ repos.Spec, _, path string, _ int64) ([]byte, error) {
		singles = append(singles, path)
		return []byte("single:" + path), nil
	}
	read := ix.untilLost(batch, single)
	ctx := context.Background()

	// When the run reads on
	first, _ := read(ctx, repos.Spec{}, "sha", "a.go", 0)
	_, missErr := read(ctx, repos.Spec{}, "sha", "gone.go", 0)
	third, err3 := read(ctx, repos.Spec{}, "sha", "c.go", 0)
	fourth, err4 := read(ctx, repos.Spec{}, "sha", "d.go", 0)

	// Then a file that is merely missing stays an error of that file, and
	// from the read that found the reader gone every file is read the slow
	// way — that one included, and the dead reader is not asked again
	if string(first) != "batch:a.go" || missErr == nil {
		t.Errorf("before the loss: read %q, miss err %v", first, missErr)
	}
	if err3 != nil || err4 != nil || string(third) != "single:c.go" || string(fourth) != "single:d.go" {
		t.Errorf("after the loss: %q (%v), %q (%v)", third, err3, fourth, err4)
	}
	if len(batched) != 3 || fmt.Sprint(singles) != "[c.go d.go]" {
		t.Errorf("batch asked %v, single asked %v", batched, singles)
	}
}

func TestACancelledRunDoesNotFallBackToSlowReads(t *testing.T) {
	// Given a reader that broke because the run was cancelled
	ix := New(Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	batch := func(context.Context, repos.Spec, string, string, int64) ([]byte, error) {
		return nil, fmt.Errorf("%w: %w", gitrepo.ErrReaderBroken, context.Canceled)
	}
	single := func(context.Context, repos.Spec, string, string, int64) ([]byte, error) {
		t.Error("a cancelled run went on reading")
		return nil, nil
	}

	// When / Then the cancellation is what comes back
	if _, err := ix.untilLost(batch, single)(ctx, repos.Spec{}, "sha", "a.go", 0); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
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
