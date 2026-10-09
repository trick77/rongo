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
	"github.com/trick77/rongo/internal/gitrepo/gittest"
	"github.com/trick77/rongo/internal/store/storetest"
)

// fixture is a checkout under root/<repo> and a database that knows it. The
// paths the index "took" have a files row; a secret has one with a
// skip_reason; a path the index never saw has none. Nothing touches a network.
type fixture struct {
	svc           *Service
	db            *sql.DB
	root          string
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
	gittest.Run(t, dir, "init", "-q", "-b", "main")
	first := gittest.Commit(t, dir, "internal/a.go", []byte("package a\n\nfunc One() {}\n"), "one")
	second := gittest.Commit(t, dir, "internal/a.go", []byte("package a\n\n// moved\nfunc One() {}\n"), "two")
	gittest.Commit(t, dir, "img.png", []byte{0x89, 'P', 'N', 'G', 0, 0xff, 0xfe}, "binary")
	gittest.Commit(t, dir, "notes.txt", []byte("caf\xe9 latin-1\n"), "latin1")
	gittest.Commit(t, dir, "config/prod.env", []byte("TOKEN=hunter2\n"), "secret")
	head := gittest.Commit(t, dir, "prod/application.properties", []byte("acme.cron.send-digest=0 0 * ? * * *\ndb.password=ENC(fixture-cipher)\nacme.key=${MASTER_KEY}\n"), "config")

	db := storetest.Open(t, 4)
	if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, branch, enabled, last_sha) VALUES ('peeq', 'x', 'main', 1, ?)`, head); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ path, sha, skip string }{
		{"internal/a.go", second, ""},
		{"img.png", head, ""},
		{"notes.txt", head, ""},
		{"config/prod.env", head, "secret"},
		{"prod/application.properties", head, ""},
	} {
		if _, err := db.Exec(`INSERT INTO files (repo, path, sha, skip_reason) VALUES ('peeq', ?, ?, ?)`, row.path, row.sha, row.skip); err != nil {
			t.Fatal(err)
		}
	}
	client := gitrepo.New(gitBin, root)
	return fixture{svc: New(db, client, maxBytes).WithCommits(client), db: db, root: root, first: first, second: second}
}

// TestReadRecorded_servesAFileTheIndexNoLongerListsButNeverOneItSkips: an
// answer read internal/a.go; a later poll deleted it and purged the row.
// The record still reads it at its commit. A file the index now SKIPS
// stays refused — that verdict is the permission — and so does a record
// with no commit.
func TestReadRecorded_servesAFileTheIndexNoLongerListsButNeverOneItSkips(t *testing.T) {
	f := newFixture(t, 1<<20)
	ctx := context.Background()
	if _, err := f.db.Exec(`DELETE FROM files WHERE path = 'internal/a.go'`); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.ReadRecorded(ctx, "peeq", "internal/a.go", f.first)
	if err != nil || !strings.Contains(got.Content, "func One") {
		t.Fatalf("ReadRecorded = %q, %v", got.Content, err)
	}
	if _, err := f.svc.Read(ctx, "peeq", "internal/a.go", f.first); !errors.Is(err, ErrNotFound) || !errors.Is(err, ErrNotIndexed) {
		t.Errorf("the viewer serves an unlisted path, or does not say it is unlisted: %v", err)
	}
	if _, err := f.svc.ReadRecorded(ctx, "peeq", "config/prod.env", f.first); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotIndexed) {
		t.Errorf("a skipped file was served, or reads as merely unlisted: %v", err)
	}
	if _, err := f.svc.ReadRecorded(ctx, "peeq", "internal/a.go", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("no commit: %v", err)
	}
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

func TestRead_aConfigurationFileIsServedRedacted(t *testing.T) {
	// Given: a properties file the indexer took, whose password line it
	// redacted before chunking. The viewer reads git, not the chunk, so it
	// has to apply the same redaction or a citation is one click from the
	// value the index never held.
	f := newFixture(t, 1<<20)

	// When
	got, err := f.svc.Read(context.Background(), "peeq", "prod/application.properties", "")

	// Then
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.Contains(got.Content, "fixture-cipher") {
		t.Fatalf("the viewer served the credential:\n%s", got.Content)
	}
	want := "acme.cron.send-digest=0 0 * ? * * *\ndb.password=<redacted>\nacme.key=${MASTER_KEY}\n"
	if got.Content != want {
		t.Fatalf("content = %q, want %q", got.Content, want)
	}
}

// TestRead_refusesAnOlderCommitWhoseBodyTheIndexerWouldSkip: the files row
// grants a path at whatever commit, so a credential committed once and removed
// since is one old sha away. The body read is judged again the way the
// selector judges it, never only redacted.
func TestRead_refusesAnOlderCommitWhoseBodyTheIndexerWouldSkip(t *testing.T) {
	// Given: internal/aws.go is indexed today; an older commit held a key.
	f := newFixture(t, 1<<20)
	dir := filepath.Join(f.root, "peeq")
	leaky := gittest.Commit(t, dir, "internal/aws.go", []byte("package a\n\nconst key = \"AKIAABCDEFGHIJKLMNOP\"\n"), "oops")
	clean := gittest.Commit(t, dir, "internal/aws.go", []byte("package a\n\nvar key = os.Getenv(\"K\")\n"), "fix")
	if _, err := f.db.Exec(`INSERT INTO files (repo, path, sha, skip_reason) VALUES ('peeq', 'internal/aws.go', ?, '')`, clean); err != nil {
		t.Fatal(err)
	}

	// When
	got, err := f.svc.Read(context.Background(), "peeq", "internal/aws.go", leaky)

	// Then
	if !errors.Is(err, ErrNotFound) || strings.Contains(got.Content, "AKIA") {
		t.Fatalf("Read at the leaky commit = %q, %v; want ErrNotFound", got.Content, err)
	}
	if got, err := f.svc.Read(context.Background(), "peeq", "internal/aws.go", clean); err != nil || !strings.Contains(got.Content, "Getenv") {
		t.Fatalf("Read at the clean commit = %q, %v", got.Content, err)
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
