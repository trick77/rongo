package gitrepo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// gitRun runs git in dir with a deterministic identity, so a developer's own
// git config cannot change what these tests build.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fixtureRepo builds a real local repository whose default branch is main. The
// "remote" is a directory on disk, so no test ever touches the network.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	writeAndCommit(t, dir, "a.txt", "first\n", "first")
	return dir
}

func writeAndCommit(t *testing.T, dir, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-qm", msg)
}

func newClient(t *testing.T) *Client {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	return New(gitBin, t.TempDir())
}

func TestDefaultBranch_resolvesFromRemoteNotAssumed(t *testing.T) {
	// Given: a remote whose default branch is main, not master.
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Enabled: true}

	// When
	branch, err := c.DefaultBranch(context.Background(), spec, "")

	// Then
	if err != nil {
		t.Fatalf("DefaultBranch() err = %v", err)
	}
	if branch != "main" {
		t.Errorf("DefaultBranch() = %q, want %q — never assume master", branch, "main")
	}
}

func TestChangedPaths_reportsOnlyTheDiff(t *testing.T) {
	// Given
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	before, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}

	// When: exactly one new file upstream.
	writeAndCommit(t, src, "b.txt", "second\n", "second")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}
	after, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}
	changed, err := c.ChangedPaths(ctx, spec, before, after)

	// Then
	if err != nil {
		t.Fatalf("ChangedPaths() err = %v", err)
	}
	if before == after {
		t.Fatal("HeadSHA did not move after a new upstream commit")
	}
	if len(changed) != 1 || changed[0] != "b.txt" {
		t.Errorf("ChangedPaths() = %v, want exactly [b.txt] — a needless full re-index is the bug this prevents", changed)
	}
}

func TestChangedEntries_separatesDeletionsFromModifications(t *testing.T) {
	// Given: a repository with two files.
	src := fixtureRepo(t)
	writeAndCommit(t, src, "gone.txt", "doomed\n", "add gone")
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	before, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}

	// When: one file is modified and the other deleted in the same push.
	writeAndCommit(t, src, "a.txt", "changed\n", "modify a")
	gitRun(t, src, "rm", "-q", "gone.txt")
	gitRun(t, src, "commit", "-qm", "delete gone")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}
	after, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}
	changes, err := c.ChangedEntries(ctx, spec, before, after)

	// Then: --name-only cannot tell these apart, and an indexer that guesses
	// either keeps answering about deleted code or drops files that still exist.
	if err != nil {
		t.Fatalf("ChangedEntries() err = %v", err)
	}
	got := map[string]bool{}
	for _, ch := range changes {
		got[ch.Path] = ch.Deleted
	}
	if len(got) != 2 {
		t.Fatalf("ChangedEntries() = %v, want two entries", changes)
	}
	if got["a.txt"] {
		t.Error("a.txt is reported as deleted, but it was modified")
	}
	if !got["gone.txt"] {
		t.Error("gone.txt is not reported as deleted; its chunks would keep answering queries")
	}
}

func TestHeadSHA_reportsErrBranchGone(t *testing.T) {
	// Given: a configured branch that does not exist upstream. This must be an
	// identifiable error — a silent stop freezes the index while every status
	// looks healthy, and answers then come from months-old code.
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "release-2024.3", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}

	// When
	_, err := c.HeadSHA(ctx, spec, "release-2024.3")

	// Then
	if !errors.Is(err, ErrBranchGone) {
		t.Fatalf("HeadSHA() err = %v, want ErrBranchGone", err)
	}
}

func TestReadFile_readsFromTheIndexedCommit(t *testing.T) {
	// Given
	src := fixtureRepo(t)
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

	// When
	body, err := c.ReadFile(ctx, spec, sha, "a.txt")

	// Then
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	if string(body) != "first\n" {
		t.Errorf("ReadFile() = %q, want %q", body, "first\n")
	}
}

func TestListPaths_listsTrackedFilesAtCommit(t *testing.T) {
	// Given: two files at HEAD.
	src := fixtureRepo(t)
	writeAndCommit(t, src, "b.txt", "second\n", "second")
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

	// When
	paths, err := c.ListPaths(ctx, spec, sha)

	// Then
	if err != nil {
		t.Fatalf("ListPaths() err = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("ListPaths() = %v, want two entries", paths)
	}
}

func TestRedact_removesEmbeddedCredentials(t *testing.T) {
	// Given: git error output that quotes the remote it failed against. The
	// token is injected into that URL for the duration of one command, and this
	// text is about to be logged.
	in := "fatal: could not read from https://x-access-token:ghp_realsecret@example.invalid/a.git"

	// When
	out := redact(in)

	// Then
	if want := "ghp_realsecret"; contains(out, want) {
		t.Errorf("redact() = %q, still contains the token", out)
	}
	if !contains(out, "REDACTED") {
		t.Errorf("redact() = %q, want the credential replaced with REDACTED", out)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}

// dumbHTTPRemote publishes a repository over plain HTTP, which is the only
// scheme authURL injects a token into — a local path never carries one, so a
// file-path fixture cannot exercise the credential path at all. "Dumb" HTTP
// needs no CGI and no network beyond loopback.
func dumbHTTPRemote(t *testing.T, src string) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "repo.git")
	gitRun(t, root, "clone", "--quiet", "--bare", src, bare)
	gitRun(t, bare, "update-server-info")
	srv := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(srv.Close)
	return srv.URL + "/repo.git"
}

func TestEnsureCloned_leavesNoCredentialInGitConfig(t *testing.T) {
	// Given: a clone over http carrying a token in its URL. git PERSISTS the
	// clone URL as remote.origin.url, so without a fix-up the token sits in
	// .git/config on disk from then on — the one place the invariant says it
	// must never be, and every later command passes its own URL anyway.
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: dumbHTTPRemote(t, src), Branch: "main", Enabled: true}

	// When
	if err := c.EnsureCloned(context.Background(), spec, "ghp_realsecret"); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}

	// Then
	body, err := os.ReadFile(filepath.Join(c.Dir(spec), ".git", "config"))
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if contains(string(body), "ghp_realsecret") {
		t.Errorf(".git/config holds the token:\n%s", body)
	}
	if !contains(string(body), spec.CloneURL) {
		t.Errorf(".git/config lost the remote entirely:\n%s", body)
	}
}

func TestListPaths_handlesNonAsciiFilenames(t *testing.T) {
	// Given: git C-quotes any path with a non-ASCII byte by default
	// ("f\303\244hig.go"), and the quoted form is NOT a path — git show then
	// fails on it, so the file drops out of the index while merely looking
	// unreadable. A German corpus is full of these.
	src := fixtureRepo(t)
	writeAndCommit(t, src, "verfügbarkeit.go", "package a\n", "umlaut")
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

	// When
	paths, err := c.ListPaths(ctx, spec, sha)
	if err != nil {
		t.Fatalf("ListPaths() err = %v", err)
	}

	// Then: the name comes back usable, and it round-trips through ReadFile.
	var found string
	for _, p := range paths {
		if contains(p, "verf") {
			found = p
		}
	}
	if found != "verfügbarkeit.go" {
		t.Fatalf("ListPaths() returned %q, want the decoded name; paths = %v", found, paths)
	}
	if _, err := c.ReadFile(ctx, spec, sha, found); err != nil {
		t.Errorf("ReadFile(%q) err = %v — the listed path is not usable", found, err)
	}
}

func TestChangedPaths_emptyDiffIsEmptyNotNil(t *testing.T) {
	// Given: a new commit that changes no file (an empty commit, an amend, a
	// revert to the same tree). A nil path list means "index everything" to the
	// pipeline, so nil here re-reads and re-chunks the entire repository.
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	before, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}
	gitRun(t, src, "commit", "-q", "--allow-empty", "-m", "empty")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}
	after, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}

	// When
	changed, err := c.ChangedPaths(ctx, spec, before, after)

	// Then
	if err != nil {
		t.Fatalf("ChangedPaths() err = %v", err)
	}
	if changed == nil {
		t.Error("ChangedPaths() = nil for an empty diff; the pipeline reads nil as a full re-index")
	}
	if len(changed) != 0 {
		t.Errorf("ChangedPaths() = %v, want no paths", changed)
	}
}

func TestHeadSHA_aBrokenCheckoutIsNotReportedAsAMissingBranch(t *testing.T) {
	// Given: a checkout directory that is not a repository at all. Reporting
	// this as ErrBranchGone puts a confident wrong diagnosis on the Repos page
	// ("configured branch not found") and discards the real error.
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: "/nonexistent", Branch: "main", Enabled: true}
	if err := os.MkdirAll(c.Dir(spec), 0o755); err != nil {
		t.Fatal(err)
	}

	// When
	_, err := c.HeadSHA(context.Background(), spec, "main")

	// Then
	if err == nil {
		t.Fatal("HeadSHA() err = nil, want a failure")
	}
	if errors.Is(err, ErrBranchGone) {
		t.Errorf("HeadSHA() err = %v, want the real error rather than ErrBranchGone", err)
	}
}
