package indexer

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
)

// capture collects log records so a test can assert on what a run SAID, not
// only on what it wrote to the database. A silent healthy run is the defect
// these lines exist to fix, so it has to be a defect a test can see.
type capture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }

func (c *capture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}

func (c *capture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capture) WithGroup(string) slog.Handler      { return c }

// find returns the first record with this message, and whether there was one.
func (c *capture) find(msg string) (slog.Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

func (c *capture) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, r := range c.records {
		out = append(out, r.Message)
	}
	return out
}

// attr reads one attribute off a record.
func attr(r slog.Record, key string) (slog.Value, bool) {
	var v slog.Value
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v, found = a.Value, true
			return false
		}
		return true
	})
	return v, found
}

func loggingPoller(t *testing.T, s *StateStore, idx IndexFunc) (*Poller, string, *capture) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	cap := &capture{}
	return NewPoller(PollerDeps{
		State:  s,
		Git:    gitrepo.New(gitBin, root),
		Index:  idx,
		Tokens: func(string) string { return "" },
		Logger: slog.New(cap),
	}), root, cap
}

// TestPollOnce_reportsWhatItIndexed: a successful index used to log nothing at
// all, so "the index is stale" and "the poller stopped running" produced
// identical output — none. This is the line somebody looks for when they ask
// whether a push has landed in the answers yet.
func TestPollOnce_reportsWhatItIndexed(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{snapshotSpec("acme-core")}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	p, root, cap := loggingPoller(t, s, (&recordingIndex{}).fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n"})

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}

	// Then
	rec, ok := cap.find("repository indexed")
	if !ok {
		t.Fatalf("no 'repository indexed' line; logged %v", cap.messages())
	}
	for key, want := range map[string]string{"repo": "acme-core", "mode": "full"} {
		got, found := attr(rec, key)
		if !found || got.String() != want {
			t.Errorf("%s = %v, want %q", key, got, want)
		}
	}
	for _, key := range []string{"sha", "files", "chunks", "took"} {
		if _, found := attr(rec, key); !found {
			t.Errorf("'repository indexed' carries no %s", key)
		}
	}
}

// TestPollOnce_bracketsTheCycleWithCounts: the summary is what makes an
// unhealthy run obvious without reading every repository's own line.
func TestPollOnce_bracketsTheCycleWithCounts(t *testing.T) {
	// Given: one repository that indexes, and a second poll where nothing moved
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{snapshotSpec("acme-core")}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	p, root, cap := loggingPoller(t, s, (&recordingIndex{}).fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n"})
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}

	// When: a cycle with nothing to do
	cap.records = nil
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then
	if _, ok := cap.find("poll cycle started"); !ok {
		t.Errorf("no 'poll cycle started' line; logged %v", cap.messages())
	}
	rec, ok := cap.find("poll cycle finished")
	if !ok {
		t.Fatalf("no 'poll cycle finished' line; logged %v", cap.messages())
	}
	for key, want := range map[string]int64{
		"checked": 1, "indexed": 0, "unchanged": 1, "failed": 0,
	} {
		got, found := attr(rec, key)
		if !found || got.Int64() != want {
			t.Errorf("%s = %v, want %d", key, got, want)
		}
	}
	// An untouched repository does not earn an INFO line of its own; the count
	// above already carries it.
	if _, ok := cap.find("repository unchanged"); ok {
		for _, r := range cap.records {
			if r.Message == "repository unchanged" && r.Level >= slog.LevelInfo {
				t.Errorf("'repository unchanged' logged at %v, want debug", r.Level)
			}
		}
	}
}

// TestPollOnce_countsAFailureWithoutStoppingTheCycle: one unreachable
// repository must be visible as a failure AND leave the rest of the corpus
// reported.
func TestPollOnce_countsAFailureWithoutStoppingTheCycle(t *testing.T) {
	// Given: a snapshot whose drop was never extracted, beside a healthy one
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		snapshotSpec("acme-core"),
		snapshotSpec("missing-drop"),
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	p, root, cap := loggingPoller(t, s, (&recordingIndex{}).fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n"})

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}

	// Then
	rec, ok := cap.find("poll cycle finished")
	if !ok {
		t.Fatalf("no 'poll cycle finished' line; logged %v", cap.messages())
	}
	for key, want := range map[string]int64{"checked": 2, "indexed": 1, "failed": 1} {
		got, found := attr(rec, key)
		if !found || got.Int64() != want {
			t.Errorf("%s = %v, want %d", key, got, want)
		}
	}
	failure, ok := cap.find("repository poll failed")
	if !ok {
		t.Fatal("no 'repository poll failed' line")
	}
	if got, _ := attr(failure, "repo"); got.String() != "missing-drop" {
		t.Errorf("failure names %v, want missing-drop", got)
	}
}

// TestPollOnce_saysWhenAnIndexWasIncremental: full and incremental are the
// difference between a push costing a re-index of everything and of one file,
// and reading which happened is the whole reason to log the mode.
func TestPollOnce_saysWhenAnIndexWasIncremental(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{snapshotSpec("acme-core")}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	p, root, cap := loggingPoller(t, s, (&recordingIndex{}).fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n"})
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}

	// When: a newer archive lands over the drop
	cap.records = nil
	extract(t, root, "acme-core", map[string]string{"a.go": "package a // v2\n"})
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then
	rec, ok := cap.find("repository indexed")
	if !ok {
		t.Fatalf("no 'repository indexed' line; logged %v", cap.messages())
	}
	if got, _ := attr(rec, "mode"); got.String() != "incremental" {
		t.Errorf("mode = %v, want incremental", got)
	}
}

// TestPollOnce_neverLogsATokenOrACredential: these lines carry a clone_url and
// an error verbatim, and both are places a secret has leaked before.
func TestPollOnce_neverLogsATokenOrACredential(t *testing.T) {
	// Given: a repository whose remote does not exist, so the failure path runs
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "private", CloneURL: "https://forge.example.invalid/acme/private.git",
			Branch: "main", TokenEnv: "TEST_FORGE_TOKEN", Enabled: true, Project: "private"},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	p, _, cap := loggingPoller(t, s, (&recordingIndex{}).fn)
	p.tokens = func(string) string { return "ghp_supersecrettokenvalue0000000000000000" }

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}

	// Then
	cap.mu.Lock()
	defer cap.mu.Unlock()
	for _, r := range cap.records {
		var sb strings.Builder
		sb.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			sb.WriteString(" " + a.Key + "=" + a.Value.String())
			return true
		})
		if strings.Contains(sb.String(), "ghp_supersecret") {
			t.Fatalf("a log line carries the token: %s", sb.String())
		}
	}
}
