package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
)

// snapshotPoller builds a poller over a repository root the test can write
// drops into, which newPoller's anonymous t.TempDir() does not expose.
func snapshotPoller(t *testing.T, s *StateStore, idx IndexFunc) (*Poller, string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	return NewPoller(PollerDeps{
		State:  s,
		Git:    gitrepo.New(gitBin, root),
		Index:  idx,
		Tokens: func(string) string { return "" },
	}), root
}

// extract writes an archive's worth of files where a snapshot's drop belongs.
func extract(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	for path, body := range files {
		full := filepath.Join(root, name, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotSpec(name string) repos.Spec {
	return repos.Spec{Name: name, Snapshot: true, Enabled: true, Project: name}
}

// stateOf reads one repository's row back.
func stateOf(t *testing.T, s *StateStore, name string) RepoState {
	t.Helper()
	all, err := s.All(context.Background())
	if err != nil {
		t.Fatalf("All() err = %v", err)
	}
	for _, st := range all {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("no repo_state row for %s", name)
	return RepoState{}
}

// TestPollOnce_snapshotIndexesTheDropOnce: the first poll commits the extracted
// tree and indexes everything, with no clone, no fetch and no default-branch
// resolution — there is no remote to ask, and asking one is exactly what failed
// before this existed.
func TestPollOnce_snapshotIndexesTheDropOnce(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{snapshotSpec("acme-core")}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p, root := snapshotPoller(t, s, rec.fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n"})

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}

	// Then
	if len(rec.calls) != 1 {
		t.Fatalf("index called %d times, want 1", len(rec.calls))
	}
	if rec.calls[0].Paths != nil {
		t.Errorf("Paths = %v, want nil for a first full index", rec.calls[0].Paths)
	}
	st := stateOf(t, s, "acme-core")
	if st.LastSHA == "" {
		t.Error("LastSHA is empty, want the snapshot commit")
	}
	if st.Branch != "snapshot" {
		t.Errorf("Branch = %q, want %q", st.Branch, "snapshot")
	}
	if st.LastError != "" {
		t.Errorf("LastError = %q, want empty", st.LastError)
	}
}

// TestPollOnce_snapshotIsOneOff: the reason snapshots exist. An untouched drop
// must cost nothing on every later cycle — same commit, no index call, and the
// run still counts as a success.
func TestPollOnce_snapshotIsOneOff(t *testing.T) {
	// Given: a snapshot already indexed
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{snapshotSpec("acme-core")}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p, root := snapshotPoller(t, s, rec.fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n"})
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}
	first := stateOf(t, s, "acme-core").LastSHA

	// When: nothing was re-extracted
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then
	if len(rec.calls) != 1 {
		t.Errorf("index called %d times, want 1 — an untouched drop must not re-index", len(rec.calls))
	}
	if got := stateOf(t, s, "acme-core").LastSHA; got != first {
		t.Errorf("LastSHA = %q, want the unchanged %q", got, first)
	}
}

// TestPollOnce_reExtractedDropIsIncremental: extracting a newer archive over the
// drop costs a diff, not a full re-index — the two commits share an object store.
func TestPollOnce_reExtractedDropIsIncremental(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{snapshotSpec("acme-core")}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p, root := snapshotPoller(t, s, rec.fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}

	// When: a newer archive lands over it
	extract(t, root, "acme-core", map[string]string{"b.go": "package b // v2\n"})
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then
	if len(rec.calls) != 2 {
		t.Fatalf("index called %d times, want 2", len(rec.calls))
	}
	if got := rec.calls[1].Paths; len(got) != 1 || got[0] != "b.go" {
		t.Errorf("Paths = %v, want [b.go]", got)
	}
}

// TestPollOnce_replacedDropReIndexesInFull: the obvious refresh is deleting the
// directory and extracting the new archive. The fresh `git init` has a NEW object
// store, so the recorded last_sha names a commit that no longer exists — diffing
// against it fails with "bad object" on every cycle, forever. Reset instead.
func TestPollOnce_replacedDropReIndexesInFull(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{snapshotSpec("acme-core")}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p, root := snapshotPoller(t, s, rec.fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n"})
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("first PollOnce() err = %v", err)
	}
	old := stateOf(t, s, "acme-core").LastSHA

	// When: the whole directory is replaced, history and all
	if err := os.RemoveAll(filepath.Join(root, "acme-core")); err != nil {
		t.Fatal(err)
	}
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n", "c.go": "package c\n"})
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then
	if len(rec.calls) != 2 {
		t.Fatalf("index called %d times, want 2", len(rec.calls))
	}
	if rec.calls[1].Paths != nil {
		t.Errorf("Paths = %v, want nil — the old commit is gone, so nothing can be diffed", rec.calls[1].Paths)
	}
	st := stateOf(t, s, "acme-core")
	if st.LastSHA == old || st.LastSHA == "" {
		t.Errorf("LastSHA = %q, want a commit from the new object store", st.LastSHA)
	}
	if st.LastError != "" {
		t.Errorf("LastError = %q, want empty", st.LastError)
	}
}

// TestPollOnce_missingDropIsRecordedLoudly: a directory nobody extracted into is
// the ordinary setup mistake. It must reach the Repos page, not sit as an empty
// index that looks healthy.
func TestPollOnce_missingDropIsRecordedLoudly(t *testing.T) {
	// Given: an entry with nothing extracted for it
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{snapshotSpec("acme-core")}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p, _ := snapshotPoller(t, s, rec.fn)

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v, want the cycle to continue", err)
	}

	// Then
	if len(rec.calls) != 0 {
		t.Errorf("index called %d times, want 0", len(rec.calls))
	}
	st := stateOf(t, s, "acme-core")
	if !strings.Contains(st.LastError, "extract the source archive") {
		t.Errorf("LastError = %q, want it to say what to do", st.LastError)
	}
}

// TestPollOnce_snapshotAndRemoteSideBySide: one cycle, both kinds. The snapshot
// must not send the remote down its path or the other way round.
func TestPollOnce_snapshotAndRemoteSideBySide(t *testing.T) {
	// Given
	src := fixtureRemote(t)
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		snapshotSpec("acme-core"),
		{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true, Project: "fixture"},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	rec := &recordingIndex{}
	p, root := snapshotPoller(t, s, rec.fn)
	extract(t, root, "acme-core", map[string]string{"a.go": "package a\n"})

	// When
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}

	// Then
	if len(rec.calls) != 2 {
		t.Fatalf("index called %d times, want 2", len(rec.calls))
	}
	if got := stateOf(t, s, "acme-core").Branch; got != "snapshot" {
		t.Errorf("acme-core Branch = %q, want snapshot", got)
	}
	if got := stateOf(t, s, "fixture").Branch; got != "main" {
		t.Errorf("fixture Branch = %q, want main", got)
	}
}
