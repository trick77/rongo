package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
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
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
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
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
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
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
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
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
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
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
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

// A fresh deployment is restarted several times while its configuration is
// being fixed, and each restart used to re-arm a full DefaultPollInterval:
// half an hour of a healthy-looking process that clones nothing.
func TestNewPoller_firstDelayIsShorterThanTheInterval(t *testing.T) {
	// Given a poller built the way main.go builds it
	testee := newPoller(t, nil, nil)

	// Then
	if testee.firstDelay != FirstPollDelay {
		t.Errorf("firstDelay = %v, want %v", testee.firstDelay, FirstPollDelay)
	}
	if testee.interval != DefaultPollInterval {
		t.Errorf("interval = %v, want %v", testee.interval, DefaultPollInterval)
	}
	if testee.firstDelay >= testee.interval {
		t.Errorf("firstDelay %v is not shorter than interval %v", testee.firstDelay, testee.interval)
	}
}

// Run must reach its first cycle on FirstDelay rather than sitting out a whole
// interval, which is what made "indexing is broken" indistinguishable from
// "indexing has not started yet".
func TestRun_pollsOnTheFirstDelayNotTheInterval(t *testing.T) {
	// Given an immediate first delay and an interval far longer than this test
	src := fixtureRemote(t)
	s := NewStateStore(newDB(t))
	if _, err := s.SyncSpecs(context.Background(), []repos.Spec{
		{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	indexed := make(chan struct{}, 1)
	testee := NewPoller(PollerDeps{
		State: s,
		Git:   gitrepo.New(gitBin, t.TempDir()),
		Index: func(context.Context, RepoState, string, []string) (Counts, error) {
			select {
			case indexed <- struct{}{}:
			default:
			}
			return Counts{Files: 1, Chunks: 2}, nil
		},
		Tokens:     func(string) string { return "" },
		Interval:   time.Hour,
		FirstDelay: time.Millisecond,
	})

	// When
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		testee.Run(ctx)
		close(done)
	}()

	// Then
	select {
	case <-indexed:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not index within 30s; it waited for the interval instead of the first delay")
	}
	cancel()
	<-done
}

func TestPollOnce_reClonesWhenTheCheckoutPointsAtAnotherRemote(t *testing.T) {
	// Given: a repository indexed from one remote, whose entry is then corrected
	// to point at a different one under the SAME name. This is how a checkout
	// came to serve one repository's code under another's name: the directory is
	// named after the entry, and until this check nothing compared the two.
	first := fixtureRemote(t)
	second := fixtureRemote(t)
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: first, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}
	// The recording index writes nothing, so the content of that first run is
	// seeded here — it is what has to be gone afterwards.
	if err := NewWriter(db).ReplaceFile(ctx, "fixture", "src/A.java", "sha", "java", 10,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil, nil); err != nil {
		t.Fatalf("ReplaceFile() err = %v", err)
	}

	// When: the entry now names the other remote.
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: second, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("second SyncSpecs() err = %v", err)
	}
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then: the checkout came from the new remote ...
	origin, err := p.git.OriginURL(ctx, repos.Spec{Name: "fixture", CloneURL: second, Enabled: true})
	if err != nil {
		t.Fatalf("OriginURL() err = %v", err)
	}
	if origin != second {
		t.Errorf("origin = %q, want the corrected %q", origin, second)
	}
	// ... it was indexed in full rather than diffed against a commit belonging
	// to the previous repository ...
	if len(rec.calls) != 2 {
		t.Fatalf("index called %d times, want 2", len(rec.calls))
	}
	if rec.calls[1].Paths != nil {
		t.Errorf("Paths = %v, want nil — a re-clone is a full index", rec.calls[1].Paths)
	}
	// ... and nothing built from the old remote survived, mirrors included.
	for _, q := range []string{
		`SELECT COUNT(*) FROM files`,
		`SELECT COUNT(*) FROM chunks`,
		`SELECT COUNT(*) FROM chunks_vec`,
		`SELECT COUNT(*) FROM chunks_fts`,
	} {
		if n := countOf(t, db, q); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
}

func TestPollOnce_reResolvesTheBranchAfterARemoteChange(t *testing.T) {
	// Given: an entry naming no branch, indexed from a remote whose default is
	// main, then corrected to one whose default is master. The corpus mixes the
	// two, so this is the ordinary case rather than a contrived one — and if the
	// resolved main were carried over, HeadSHA would report the branch gone on
	// every cycle with nothing left to re-resolve it.
	first := fixtureRemote(t)
	second := t.TempDir()
	gitRun(t, second, "init", "-q", "-b", "master")
	writeAndCommit(t, second, "b.txt", "second\n", "second")

	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: first, Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}

	// When
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: second, Enabled: true},
	}); err != nil {
		t.Fatalf("second SyncSpecs() err = %v", err)
	}
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then: the branch is the new remote's default, and the repository indexed
	// rather than stalling on a branch that does not exist there.
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}
	if len(active) != 1 || active[0].Branch != "master" {
		t.Fatalf("branch = %q, want master resolved from the new remote", active[0].Branch)
	}
	if active[0].LastError != "" {
		t.Errorf("LastError = %q, want none", active[0].LastError)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("index called %d times, want 2", len(rec.calls))
	}
}

func TestPollOnce_recordsAnUnreadableCheckoutInsteadOfIndexingIt(t *testing.T) {
	// A checkout git cannot read cannot be compared against the entry's URL, so
	// the identity of what is on disk is unknown. The poll fails loudly and the
	// Repos page says so; indexing it anyway would file whatever is there under
	// this entry's name.
	src := fixtureRemote(t)
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)
	dir := p.git.Dir(repos.Spec{Name: "fixture"})
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v, want the failure recorded per repository", err)
	}

	// Then
	if len(rec.calls) != 0 {
		t.Errorf("index called %d times, want none", len(rec.calls))
	}
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All() err = %v", err)
	}
	if len(all) != 1 || all[0].LastError == "" {
		t.Errorf("LastError = %q, want the git failure on the Repos page", all[0].LastError)
	}
}

func TestPollOnce_leavesAMatchingCheckoutAlone(t *testing.T) {
	// The check must not fire on the ordinary case: the URL is unchanged, so the
	// second poll finds nothing new and the index is not thrown away.
	src := fixtureRemote(t)
	db := purgeDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	if _, err := s.SyncSpecs(ctx, []repos.Spec{spec}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p := newPoller(t, s, rec.fn)
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}
	if err := NewWriter(db).ReplaceFile(ctx, "fixture", "src/A.java", "sha", "java", 10,
		sampleChunks(), [][]float32{vec(1), vec(2)}, nil, nil); err != nil {
		t.Fatalf("ReplaceFile() err = %v", err)
	}

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then
	if len(rec.calls) != 1 {
		t.Errorf("index called %d times, want 1 — the second poll re-indexed a checkout that was correct", len(rec.calls))
	}
	if n := countOf(t, db, `SELECT COUNT(*) FROM chunks`); n != 2 {
		t.Errorf("chunks = %d, want the 2 that were there", n)
	}
}
