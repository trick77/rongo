package indexer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
)

// byteCountingGit is the harness's git with every byte a read hands over
// counted per path. It offers no batch reader, so every read is seen here.
type byteCountingGit struct {
	GitClient
	mu    sync.Mutex
	bytes map[string]int
}

func (g *byteCountingGit) ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error) {
	body, err := g.GitClient.ReadFile(ctx, spec, sha, path)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bytes[path] += len(body)
	return body, err
}

func (g *byteCountingGit) read(path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bytes[path]
}

func bigGo(lines int) string {
	return "package big\n\n" + strings.Repeat("// filler line of a generated table\n", lines)
}

// TestIndexOne_skipsAnOversizedBlobWithoutReadingIt: the size ceiling used to
// be checked on the body, after git had handed over every byte; one 2 GB
// artifact took the poller down. The listing says how big a blob is, so the
// verdict comes before the read, on a full run and an incremental one.
func TestIndexOne_skipsAnOversizedBlobWithoutReadingIt(t *testing.T) {
	// Given a tree with one file over a 1 KiB ceiling
	h := newHarnessFiles(t, map[string]string{
		"big.go":   bigGo(100),
		"small.go": "package small\n\nfunc Small() {}\n",
	}, nil)
	h.ix.selector = NewSelector(SelectOptions{MaxBytes: 1024})
	git := &byteCountingGit{GitClient: h.gitc, bytes: map[string]int{}}
	h.ix.git = git
	st := h.stateOf(t)
	first := h.head(t)

	// When indexed in full
	if _, err := h.ix.IndexRepo(context.Background(), st, first, nil); err != nil {
		t.Fatalf("full IndexRepo() err = %v", err)
	}

	// Then it is recorded as too large at its size, and nothing was read
	assertTooLarge(t, h, "big.go", len(bigGo(100)))
	if n := git.read("big.go"); n != 0 {
		t.Errorf("full run read %d bytes of big.go, want 0", n)
	}
	if git.read("small.go") == 0 {
		t.Error("small.go was never read; the counter sees nothing")
	}

	// When it grows and the run is incremental
	write(t, h.src, "big.go", bigGo(200))
	git2(t, h.src)
	st.LastSHA = first
	if _, err := h.ix.IndexRepo(context.Background(), st, h.head(t), []string{"big.go"}); err != nil {
		t.Fatalf("incremental IndexRepo() err = %v", err)
	}

	// Then the same, at the new size
	assertTooLarge(t, h, "big.go", len(bigGo(200)))
	if n := git.read("big.go"); n != 0 {
		t.Errorf("incremental run read %d bytes of big.go, want 0", n)
	}
}

// TestIndexRepo_aBlobOfUnknownSizeIsRefusedByTheReader: bare paths carry no
// size, so the run's batch reader enforces the ceiling itself, skipping the
// blob on its stream, and the refusal is recorded like the listing's verdict.
func TestIndexRepo_aBlobOfUnknownSizeIsRefusedByTheReader(t *testing.T) {
	h := newHarnessFiles(t, map[string]string{
		"big.go":   bigGo(100),
		"small.go": "package small\n\nfunc Small() {}\n",
	}, nil)
	h.ix.selector = NewSelector(SelectOptions{MaxBytes: 1024})
	git := &countingGit{Client: h.gitc}
	h.ix.git = git
	st, sha := h.stateOf(t), h.head(t)
	ctx := context.Background()

	// The run's reader refuses past the limit it is handed and reads on.
	read, done := h.ix.reader(ctx, h.spec)
	_, err := read(ctx, h.spec, sha, "big.go", 1024)
	var tl *gitrepo.TooLargeError
	if !errors.As(err, &tl) || tl.Size != int64(len(bigGo(100))) {
		t.Errorf("read(big.go) err = %v, want a TooLargeError at its size", err)
	}
	if body, err := read(ctx, h.spec, sha, "small.go", 1024); err != nil || len(body) == 0 {
		t.Errorf("read(small.go) = %d bytes, %v; the stream fell out of step", len(body), err)
	}
	done()
	if git.singles.Load() != 0 {
		t.Errorf("read %d files a process at a time, want 0 — the refusal broke the reader", git.singles.Load())
	}

	// A refusal is recorded at the size git reported: nothing was read to
	// measure.
	refuse := func(context.Context, repos.Spec, string, string, int64) ([]byte, error) {
		return nil, fmt.Errorf("read: %w", &gitrepo.TooLargeError{Size: 5 << 20})
	}
	if err := h.ix.indexOne(ctx, h.spec, st, sha, target{path: "big.go"}, refuse); err != nil {
		t.Fatalf("indexOne() err = %v", err)
	}
	assertTooLarge(t, h, "big.go", 5<<20)
}

func assertTooLarge(t *testing.T, h *harness, path string, size int) {
	t.Helper()
	var reason string
	var got int
	if err := h.db.QueryRow(`SELECT skip_reason, size FROM files WHERE repo = 'shop' AND path = ?`, path).Scan(&reason, &got); err != nil {
		t.Fatalf("no row for %s: %v", path, err)
	}
	if reason != string(SkipTooLarge) || got != size {
		t.Errorf("%s = %q at %d bytes, want %q at %d", path, reason, got, SkipTooLarge, size)
	}
}

// git2 commits everything in dir.
func git2(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "grow")
}
