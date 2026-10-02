package gitrepo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// clonedFixture is a checkout under the client's root with a handful of files
// a reader has to get right: text, an empty file, bytes that are not text, a
// name with a space and an umlaut, and a directory.
func clonedFixture(t *testing.T) (*Client, repos.Spec, string) {
	t.Helper()
	src := fixtureRepo(t)
	writeAndCommit(t, src, "empty.txt", "", "empty")
	writeAndCommit(t, src, "fähig one.go", "package a\n\nfunc A() {}\n", "umlaut")
	if err := os.MkdirAll(filepath.Join(src, "dir"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, src, "dir/b.txt", "second\nline\n", "nested")
	if err := os.WriteFile(filepath.Join(src, "bin.dat"), []byte{0, 1, 2, '\n', 0xff, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, "add", "bin.dat")
	gitRun(t, src, "commit", "-qm", "binary")

	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	sha, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}
	return c, spec, sha
}

func TestReader_readsEveryFileExactlyAsGitShowDoes(t *testing.T) {
	// Given
	c, spec, sha := clonedFixture(t)
	ctx := context.Background()
	r, err := c.NewReader(ctx, spec)
	if err != nil {
		t.Fatalf("NewReader() err = %v", err)
	}
	defer func() { _ = r.Close() }()

	// When / Then: byte for byte what the one-process-per-file read returns,
	// in any order, more than once
	for _, p := range []string{"a.txt", "empty.txt", "fähig one.go", "dir/b.txt", "bin.dat", "a.txt"} {
		want, err := c.ReadFile(ctx, spec, sha, p)
		if err != nil {
			t.Fatalf("ReadFile(%q) err = %v", p, err)
		}
		got, err := r.ReadFile(ctx, spec, sha, p)
		if err != nil {
			t.Fatalf("Reader.ReadFile(%q) err = %v", p, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%q: read %q, want %q", p, got, want)
		}
	}
}

func TestReader_aMissingPathIsAnErrorAndTheNextReadStillWorks(t *testing.T) {
	// Given
	c, spec, sha := clonedFixture(t)
	ctx := context.Background()
	r, err := c.NewReader(ctx, spec)
	if err != nil {
		t.Fatalf("NewReader() err = %v", err)
	}
	defer func() { _ = r.Close() }()

	// When a path the commit does not have is asked for
	_, err = r.ReadFile(ctx, spec, sha, "nope.go")

	// Then that read fails, naming the path, and the reader is not broken:
	// one unreadable file must not turn every later one into a failure
	if err == nil || !strings.Contains(err.Error(), "nope.go") {
		t.Fatalf("err = %v, want one naming the path", err)
	}
	got, err := r.ReadFile(ctx, spec, sha, "a.txt")
	if err != nil || string(got) != "first\n" {
		t.Errorf("after a miss: read %q, err %v", got, err)
	}
}

func TestReader_aDirectoryIsNotReadAsAFile(t *testing.T) {
	c, spec, sha := clonedFixture(t)
	ctx := context.Background()
	r, err := c.NewReader(ctx, spec)
	if err != nil {
		t.Fatalf("NewReader() err = %v", err)
	}
	defer func() { _ = r.Close() }()

	if _, err := r.ReadFile(ctx, spec, sha, "dir"); err == nil {
		t.Error("a tree was read as a file")
	}
	// And the stream is still in step afterwards.
	got, err := r.ReadFile(ctx, spec, sha, "dir/b.txt")
	if err != nil || string(got) != "second\nline\n" {
		t.Errorf("after the tree: read %q, err %v", got, err)
	}
}

func TestReader_aPathWithANewlineGoesThroughTheSingleRead(t *testing.T) {
	// A newline ends a request on the batch's input, so such a path cannot be
	// asked for there; it is read the slow way instead of being mangled.
	c, spec, sha := clonedFixture(t)
	ctx := context.Background()
	r, err := c.NewReader(ctx, spec)
	if err != nil {
		t.Fatalf("NewReader() err = %v", err)
	}
	defer func() { _ = r.Close() }()

	_, err = r.ReadFile(ctx, spec, sha, "a\n.txt")

	if err == nil {
		t.Fatal("a path that does not exist was read")
	}
	got, err := r.ReadFile(ctx, spec, sha, "a.txt")
	if err != nil || string(got) != "first\n" {
		t.Errorf("after the newline path: read %q, err %v — the batch stream is out of step", got, err)
	}
}

func TestReader_refusesAnotherCheckoutAndAClosedReader(t *testing.T) {
	c, spec, sha := clonedFixture(t)
	ctx := context.Background()
	r, err := c.NewReader(ctx, spec)
	if err != nil {
		t.Fatalf("NewReader() err = %v", err)
	}

	// One reader is one checkout's object store.
	other := repos.Spec{Name: "other", CloneURL: spec.CloneURL, Branch: "main", Enabled: true}
	if _, err := r.ReadFile(ctx, other, sha, "a.txt"); err == nil {
		t.Error("a reader answered for a checkout it was not opened on")
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close() err = %v", err)
	}
	if _, err := r.ReadFile(ctx, spec, sha, "a.txt"); err == nil {
		t.Error("a closed reader read a file")
	}
	// Closing twice is not an error: the run's defer and an early exit may both do it.
	if err := r.Close(); err != nil {
		t.Errorf("second Close() err = %v", err)
	}
}

func TestReader_aCancelledRunStopsReading(t *testing.T) {
	c, spec, sha := clonedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	r, err := c.NewReader(ctx, spec)
	if err != nil {
		t.Fatalf("NewReader() err = %v", err)
	}
	defer func() { _ = r.Close() }()

	cancel()

	if _, err := r.ReadFile(ctx, spec, sha, "a.txt"); err == nil {
		t.Error("a read succeeded after the run was cancelled")
	}
}

func TestNewReader_aCheckoutThatIsNotThereIsAnError(t *testing.T) {
	c := newClient(t)
	spec := repos.Spec{Name: "never-cloned", Branch: "main", Enabled: true}

	r, err := c.NewReader(context.Background(), spec)
	if err == nil {
		// Starting may succeed and the first read fail, depending on when git
		// notices; either way no file comes back.
		defer func() { _ = r.Close() }()
		if _, err := r.ReadFile(context.Background(), spec, "deadbeef", "a.txt"); err == nil {
			t.Error("read a file out of a checkout that does not exist")
		}
	}
}
