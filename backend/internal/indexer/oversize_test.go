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

// TestIndexOne_decidesTheDataCeilingsUnreadWhereTheSizeIsFinal: a schema over
// its ceiling and a data format are refused on the listed size, unread. A
// configuration file is redacted before the data ceiling and the redacted
// body can fall under it, so its verdict still needs the bytes: here a long
// credential value takes a json file under the ceiling, and it is indexed.
func TestIndexOne_decidesTheDataCeilingsUnreadWhereTheSizeIsFinal(t *testing.T) {
	schema := `<?xml version="1.0"?>` + "\n" + `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">` + "\n" +
		strings.Repeat(`  <xs:element name="filler" type="xs:string"/>`+"\n", 60) + "</xs:schema>\n"
	csv := "id,name\n" + strings.Repeat("1,filler\n", 300)
	secretJSON := `{"name": "shop", "password": "` + strings.Repeat("s", 3000) + `"}` + "\n"
	h := newHarnessFiles(t, map[string]string{
		"contracts/order.xsd": schema,
		"data/rows.csv":       csv,
		"config/app.json":     secretJSON,
	}, nil)
	h.ix.selector = NewSelector(SelectOptions{MaxDataBytes: 1024, MaxSchemaBytes: 1024})
	git := &byteCountingGit{GitClient: h.gitc, bytes: map[string]int{}}
	h.ix.git = git

	if _, err := h.ix.IndexRepo(context.Background(), h.stateOf(t), h.head(t), nil); err != nil {
		t.Fatalf("IndexRepo() err = %v", err)
	}

	for p, size := range map[string]int{"contracts/order.xsd": len(schema), "data/rows.csv": len(csv)} {
		var reason string
		var got int
		if err := h.db.QueryRow(`SELECT skip_reason, size FROM files WHERE path = ?`, p).Scan(&reason, &got); err != nil {
			t.Fatalf("no row for %s: %v", p, err)
		}
		if reason != string(SkipData) || got != size {
			t.Errorf("%s = %q at %d bytes, want %q at %d", p, reason, got, SkipData, size)
		}
		if n := git.read(p); n != 0 {
			t.Errorf("read %d bytes of %s, want 0", n, p)
		}
	}
	if n := countOf(t, h.db, `SELECT COUNT(*) FROM chunks c JOIN files f ON f.id = c.file_id WHERE f.path = 'config/app.json'`); n == 0 {
		t.Error("config/app.json was not indexed; its redacted body is under the ceiling")
	}
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
