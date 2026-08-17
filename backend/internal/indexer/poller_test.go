package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
)

// gitRun runs git with a deterministic identity so a developer's own git config
// cannot change what these fixtures contain.
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

// fixtureRemote builds a local repository whose default branch is main. No test
// here touches the network.
func fixtureRemote(t *testing.T) string {
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

// recordingIndex captures what the poller asked to be indexed.
type recordingIndex struct {
	calls []indexCall
	err   error
}

type indexCall struct {
	Repo  string
	SHA   string
	Paths []string
}

func (r *recordingIndex) fn(_ context.Context, st RepoState, sha string, paths []string) (Counts, error) {
	r.calls = append(r.calls, indexCall{Repo: st.Name, SHA: sha, Paths: paths})
	if r.err != nil {
		return Counts{}, r.err
	}
	return Counts{Files: 1, Chunks: 2}, nil
}

func newPoller(t *testing.T, s *StateStore, idx IndexFunc) *Poller {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	return NewPoller(PollerDeps{
		State:  s,
		Git:    gitrepo.New(gitBin, t.TempDir()),
		Index:  idx,
		Tokens: func(string) string { return "" },
	})
}

func TestPollOnce_fullIndexOnFirstSight(t *testing.T) {
	// Given: a repository never indexed before (LastSHA empty).
	src := fixtureRemote(t)
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}

	// Then: indexed with nil paths, meaning "everything".
	if len(rec.calls) != 1 {
		t.Fatalf("index called %d times, want 1", len(rec.calls))
	}
	if rec.calls[0].Paths != nil {
		t.Errorf("Paths = %v, want nil for a first full index", rec.calls[0].Paths)
	}
}

func TestPollOnce_skipsWhenHeadIsUnchanged(t *testing.T) {
	// Given: a repository already indexed at the current HEAD.
	src := fixtureRemote(t)
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}
	first := len(rec.calls)

	// When: nothing changed upstream.
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then: no second index. Re-indexing an unchanged repository would burn the
	// embedding budget and the wall-clock for nothing.
	if len(rec.calls) != first {
		t.Errorf("index called %d times, want it left at %d for an unchanged HEAD",
			len(rec.calls), first)
	}
}

func TestPollOnce_incrementalIndexPassesOnlyChangedPaths(t *testing.T) {
	// Given: an indexed repository that then receives one new file.
	src := fixtureRemote(t)
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}

	// When
	writeAndCommit(t, src, "b.txt", "second\n", "second")
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then: exactly the changed path, not the whole tree.
	if len(rec.calls) != 2 {
		t.Fatalf("index called %d times, want 2", len(rec.calls))
	}
	got := rec.calls[1].Paths
	if len(got) != 1 || got[0] != "b.txt" {
		t.Errorf("Paths = %v, want exactly [b.txt] — a needless full re-index is the bug this prevents", got)
	}
}

func TestPollOnce_resolvesAnOmittedBranchAndRecordsIt(t *testing.T) {
	// Given: the YAML omitted the branch, so it is empty. The remote's default
	// is main, and assuming master would break exactly the third-party
	// repositories this corpus needs.
	src := fixtureRemote(t)
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: src, Branch: "", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}

	// Then
	active, _ := s.Active(ctx)
	if active[0].Branch != "main" {
		t.Errorf("Branch = %q, want %q resolved from the remote", active[0].Branch, "main")
	}
	if len(rec.calls) != 1 {
		t.Errorf("index called %d times, want 1 after resolving the branch", len(rec.calls))
	}
}

func TestPollOnce_recordsAVanishedBranchAndKeepsGoing(t *testing.T) {
	// Given: two repositories, the first configured on a branch that does not
	// exist upstream.
	src := fixtureRemote(t)
	other := fixtureRemote(t)
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "aaa-broken", CloneURL: src, Branch: "release-2024.3", Enabled: true},
		{Name: "zzz-healthy", CloneURL: other, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)

	// When
	err := p.PollOnce(ctx)

	// Then: the failure is recorded ...
	if err != nil {
		t.Fatalf("PollOnce() err = %v, want per-repository failures to be recorded, not returned", err)
	}
	active, _ := s.Active(ctx)
	var broken RepoState
	for _, r := range active {
		if r.Name == "aaa-broken" {
			broken = r
		}
	}
	if broken.LastError == "" {
		t.Error("LastError is empty for the repo whose branch vanished, want it surfaced on the Repos page")
	}

	// ... and the healthy repository was still processed. One broken remote
	// must not stall the rest of the corpus.
	if len(rec.calls) != 1 || rec.calls[0].Repo != "zzz-healthy" {
		t.Errorf("index calls = %+v, want the healthy repo indexed despite the broken one", rec.calls)
	}
}

func TestPollOnce_doesNotAdvanceTheShaWhenIndexingFails(t *testing.T) {
	// Given: indexing that fails. If the poller recorded the new SHA anyway,
	// the next run would see "unchanged" and the repository would stay
	// permanently un-indexed while looking healthy.
	src := fixtureRemote(t)
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{err: context.DeadlineExceeded}
	p := newPoller(t, s, rec.fn)

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v, want the failure recorded not returned", err)
	}

	// Then
	active, _ := s.Active(ctx)
	if active[0].LastSHA != "" {
		t.Errorf("LastSHA = %q, want it left empty so the next run retries", active[0].LastSHA)
	}
	if active[0].LastError == "" {
		t.Error("LastError is empty, want the indexing failure surfaced")
	}
}
