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
// change what the fixture looks like.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
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

// fixture builds a checkout under root/<repo> with two commits of the same
// file, and a database that knows the repository. Nothing touches a network.
func fixture(t *testing.T) (svc *Service, db *sql.DB, first, second string) {
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
	first = commit(t, dir, "internal/a.go", []byte("package a\n\nfunc One() {}\n"), "one")
	second = commit(t, dir, "internal/a.go", []byte("package a\n\n// moved\nfunc One() {}\n"), "two")
	// The repository's last indexed commit is the newest one; the a.go files
	// row stays on the second, as a file untouched by the latest run would.
	third := commit(t, dir, "img.png", []byte{0x89, 'P', 'N', 'G', 0, 0xff, 0xfe}, "binary")

	db, err = store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch, enabled, last_sha) VALUES ('peeq', 'x', 'main', 1, ?)`, third); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO files (repo, path, sha) VALUES ('peeq', 'internal/a.go', ?)`, second); err != nil {
		t.Fatal(err)
	}
	return New(db, gitrepo.New(gitBin, root)), db, first, second
}

func TestRead_showsTheFileAtTheCitedCommitNotTheBranchHead(t *testing.T) {
	// Given
	svc, _, first, _ := fixture(t)

	// When
	f, err := svc.Read(context.Background(), "peeq", "internal/a.go", first)

	// Then
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.Contains(f.Content, "moved") {
		t.Fatalf("got the branch head, want the cited commit: %q", f.Content)
	}
	if f.Branch != "main" || f.SHA != first || f.Path != "internal/a.go" {
		t.Fatalf("file = %+v", f)
	}
}

func TestRead_anEmptyCommitFallsBackToTheIndexedOne(t *testing.T) {
	// Given: a citation recorded before the commit travelled with it.
	svc, _, _, second := fixture(t)

	// When
	f, err := svc.Read(context.Background(), "peeq", "internal/a.go", "")

	// Then
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if f.SHA != second || !strings.Contains(f.Content, "moved") {
		t.Fatalf("file = %+v, want the indexed commit %s", f, second)
	}
}

func TestRead_refusesWhatItCannotShow(t *testing.T) {
	svc, _, first, _ := fixture(t)
	ctx := context.Background()

	for name, tc := range map[string]struct {
		repo, path, sha string
		want            error
	}{
		"unknown repository": {"loom", "internal/a.go", first, ErrNotFound},
		"path not at commit": {"peeq", "internal/b.go", first, ErrNotFound},
		"binary file":        {"peeq", "img.png", "", ErrBinary},
		"empty path":         {"peeq", "", first, ErrInvalid},
		"climbing path":      {"peeq", "../etc/passwd", first, ErrInvalid},
		"option as commit":   {"peeq", "internal/a.go", "--output=/tmp/x", ErrInvalid},
		"dash path":          {"peeq", "-internal/a.go", first, ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Read(ctx, tc.repo, tc.path, tc.sha)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
