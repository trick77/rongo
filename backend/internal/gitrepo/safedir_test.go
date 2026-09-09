package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// TestEveryCommandCarriesSafeDirectory pins the exemption to the ONE place that
// covers all of them.
//
// An earlier version set safe.directory at the two call sites EnsureSnapshot
// makes and left ListPaths, ChangedPaths, ChangedEntries, ReadFile and Object
// without it — which is most of what a snapshot needs. Under the deployment the
// feature documents (container as uid 1000, drop owned by whoever unpacked the
// archive) the commit would land and the very next call would fail with
// "dubious ownership", leaving a permanent error and an unreadable citation.
//
// git's own ownership check cannot be exercised here without a second uid, so
// this asserts the mechanism instead: run() is the only path to the binary, and
// it carries the flag.
func TestEveryCommandCarriesSafeDirectory(t *testing.T) {
	// Given
	dir := t.TempDir()

	// When
	got := safeDirectory(dir, "-c", "core.quotePath=false", "diff", "--name-only")

	// Then
	if len(got) < 2 || got[0] != "-c" || got[1] != "safe.directory="+dir {
		t.Fatalf("safeDirectory() = %v, want it to lead with the exemption", got)
	}
	// The caller's own -c pairs survive, and the subcommand is still findable —
	// error messages name "diff", not "-c".
	if sub := subcommand(got); sub != "diff" {
		t.Errorf("subcommand(%v) = %q, want diff", got, sub)
	}
}

// TestEnsureSnapshot_refusesADirectoryHoldingOnlyGit: `rm -rf <dir>/*` leaves
// .git behind, because the glob skips dotfiles. Committing that stages the
// deletion of every file, and the Repos page then shows a repository with 0
// files and no error — the silent empty index the guard exists to prevent.
func TestEnsureSnapshot_refusesADirectoryHoldingOnlyGit(t *testing.T) {
	// Given: an indexed snapshot whose source files are then all removed
	c := newClient(t)
	spec := dropSource(t, c, "acme-core", map[string]string{"a.go": "package a\n"})
	if _, err := c.EnsureSnapshot(context.Background(), spec); err != nil {
		t.Fatalf("EnsureSnapshot() err = %v", err)
	}
	if err := os.Remove(filepath.Join(c.Dir(spec), "a.go")); err != nil {
		t.Fatal(err)
	}

	// When
	_, err := c.EnsureSnapshot(context.Background(), spec)

	// Then
	if err == nil {
		t.Fatal("EnsureSnapshot() err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "holds no source") {
		t.Errorf("EnsureSnapshot() err = %v, want it to say the drop holds no source", err)
	}
}

// TestEnsureSnapshot_readFailureIsNotReportedAsMissing: telling an operator to
// extract an archive that is already sitting there sends them nowhere.
func TestEnsureSnapshot_readFailureIsNotReportedAsMissing(t *testing.T) {
	// Given: a FILE where the drop directory belongs
	c := newClient(t)
	spec := repos.Spec{Name: "acme-core", Snapshot: true, Enabled: true}
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Dir(spec), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// When
	_, err := c.EnsureSnapshot(context.Background(), spec)

	// Then
	if err == nil {
		t.Fatal("EnsureSnapshot() err = nil, want the read failure")
	}
	if strings.Contains(err.Error(), "does not exist") {
		t.Errorf("EnsureSnapshot() err = %v, want the real cause, not 'does not exist'", err)
	}
}

// TestOriginURL_saysWhenASnapshotSitsWhereACloneBelongs: the mirror of
// assertSnapshotCheckout. Switching an entry from snapshot: true to a clone_url
// — or re-adding a purged snapshot's name as a clone, since purge leaves the
// tree on disk — used to return git's raw "No such remote" on every cycle
// forever, with nothing saying what to do about it.
func TestOriginURL_saysWhenASnapshotSitsWhereACloneBelongs(t *testing.T) {
	// Given: a snapshot's drop, committed
	c := newClient(t)
	snap := dropSource(t, c, "acme-core", map[string]string{"a.go": "package a\n"})
	if _, err := c.EnsureSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("EnsureSnapshot() err = %v", err)
	}
	// ... and the same name now configured as a clone
	asClone := repos.Spec{
		Name:     "acme-core",
		CloneURL: "https://forge.example.invalid/acme/acme-core.git",
		Enabled:  true,
	}

	// When
	_, err := c.OriginURL(context.Background(), asClone)

	// Then
	if err == nil {
		t.Fatal("OriginURL() err = nil, want a refusal naming the directory")
	}
	if !strings.Contains(err.Error(), "remove the directory") {
		t.Errorf("OriginURL() err = %v, want it to say what to do", err)
	}
	if !strings.Contains(err.Error(), c.Dir(asClone)) {
		t.Errorf("OriginURL() err = %v, want it to name the directory", err)
	}
}

// TestOriginURL_stillReportsARealCloneUnchanged: the guard above must not
// swallow the ordinary answer.
func TestOriginURL_stillReportsARealCloneUnchanged(t *testing.T) {
	// Given
	c := newClient(t)
	source := fixtureRepo(t)
	spec := repos.Spec{Name: "fixture", CloneURL: source, Enabled: true}
	if err := c.EnsureCloned(context.Background(), spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}

	// When
	got, err := c.OriginURL(context.Background(), spec)

	// Then
	if err != nil {
		t.Fatalf("OriginURL() err = %v", err)
	}
	if got != source {
		t.Errorf("OriginURL() = %q, want %q", got, source)
	}
}
