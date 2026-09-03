package sourceview

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/store"
)

// gitRun runs git with a fixed identity, so a developer's own config cannot
// change what the fixture looks like. Automatic maintenance is off: a commit
// may otherwise detach a background gc that is still writing under .git
// while t.TempDir removes it, which failed once on CI as "directory not
// empty".
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, name string, body []byte, msg string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-qm", msg)
	return gitRun(t, dir, "rev-parse", "HEAD")
}

// fixture is a checkout under root/<repo> and a database that knows it. The
// paths the index "took" have a files row; a secret has one with a
// skip_reason; a path the index never saw has none. Nothing touches a network.
type fixture struct {
	svc           *Service
	db            *sql.DB
	first, second string
}

func newFixture(t *testing.T, maxBytes int) fixture {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "peeq")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
	first := commit(t, dir, "internal/a.go", []byte("package a\n\nfunc One() {}\n"), "one")
	second := commit(t, dir, "internal/a.go", []byte("package a\n\n// moved\nfunc One() {}\n"), "two")
	commit(t, dir, "img.png", []byte{0x89, 'P', 'N', 'G', 0, 0xff, 0xfe}, "binary")
	commit(t, dir, "notes.txt", []byte("caf\xe9 latin-1\n"), "latin1")
	head := commit(t, dir, "config/prod.env", []byte("TOKEN=hunter2\n"), "secret")

	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch, enabled, last_sha) VALUES ('peeq', 'x', 'main', 1, ?)`, head); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ path, sha, skip string }{
		{"internal/a.go", second, ""},
		{"img.png", head, ""},
		{"notes.txt", head, ""},
		{"config/prod.env", head, "secret"},
	} {
		if _, err := db.Exec(`INSERT INTO files (repo, path, sha, skip_reason) VALUES ('peeq', ?, ?, ?)`, row.path, row.sha, row.skip); err != nil {
			t.Fatal(err)
		}
	}
	return fixture{svc: New(db, gitrepo.New(gitBin, root), maxBytes), db: db, first: first, second: second}
}

func TestRead_showsTheFileAtTheCitedCommitNotTheBranchHead(t *testing.T) {
	// Given
	f := newFixture(t, 1<<20)

	// When
	got, err := f.svc.Read(context.Background(), "peeq", "internal/a.go", f.first)

	// Then
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.Contains(got.Content, "moved") {
		t.Fatalf("got the branch head, want the cited commit: %q", got.Content)
	}
	if got.Branch != "main" || got.SHA != f.first || got.Path != "internal/a.go" {
		t.Fatalf("file = %+v", got)
	}
}

func TestRead_anEmptyCommitFallsBackToTheIndexedOne(t *testing.T) {
	// Given: a citation recorded before the commit travelled with it.
	f := newFixture(t, 1<<20)

	// When
	got, err := f.svc.Read(context.Background(), "peeq", "internal/a.go", "")

	// Then
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.SHA != f.second || !strings.Contains(got.Content, "moved") {
		t.Fatalf("file = %+v, want the indexed commit %s", got, f.second)
	}
}

func TestRead_textIsWhatTheIndexerCallsText(t *testing.T) {
	// Given: a Latin-1 file. Not valid UTF-8, but no NUL byte, so the
	// indexer took it and an answer can cite it.
	f := newFixture(t, 1<<20)

	// When
	got, err := f.svc.Read(context.Background(), "peeq", "notes.txt", "")

	// Then
	if err != nil {
		t.Fatalf("Read: %v, want the file the index serves", err)
	}
	if !strings.Contains(got.Content, "latin-1") {
		t.Fatalf("content = %q", got.Content)
	}
}

func TestRead_refusesWhatItCannotShow(t *testing.T) {
	f := newFixture(t, 1<<20)
	ctx := context.Background()

	for name, tc := range map[string]struct {
		repo, path, sha string
		want            error
	}{
		"unknown repository": {"loom", "internal/a.go", f.first, ErrNotFound},
		"path never indexed": {"peeq", "internal/b.go", f.first, ErrNotFound},
		// The named door this must not be: a secret the selector refused has
		// a files row, so the answer layer can say "exists, not indexed" —
		// and that row is not permission to serve it.
		"skipped as secret": {"peeq", "config/prod.env", "", ErrNotFound},
		"a directory":       {"peeq", "internal", f.first, ErrNotFound},
		"binary file":       {"peeq", "img.png", "", ErrBinary},
		"empty path":        {"peeq", "", f.first, ErrInvalid},
		"climbing path":     {"peeq", "../etc/passwd", f.first, ErrInvalid},
		"option as commit":  {"peeq", "internal/a.go", "--output=/tmp/x", ErrInvalid},
		"dash path":         {"peeq", "-internal/a.go", f.first, ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Read(ctx, tc.repo, tc.path, tc.sha)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRead_aDirectoryIsNotServedAsAFile(t *testing.T) {
	// git show sha:dir prints a listing and exits 0, which would otherwise
	// come back as "content".
	f := newFixture(t, 1<<20)
	_, err := f.svc.Read(context.Background(), "peeq", "internal", f.first)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want %v", err, ErrNotFound)
	}
}

func TestRead_refusesALargeFileBeforeReadingIt(t *testing.T) {
	// Given: a cap below the file. The refusal comes from the object's size,
	// so nothing is buffered first.
	f := newFixture(t, 8)

	// When
	_, err := f.svc.Read(context.Background(), "peeq", "internal/a.go", "")

	// Then
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want %v", err, ErrTooLarge)
	}
}
