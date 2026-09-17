package indexer

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/history"
	"github.com/trick77/rongo/internal/repos"
)

func TestPollOnce_recordsTheHistoryFullThenIncremental(t *testing.T) {
	// Given: a remote with two commits, a poller with the commit lane wired.
	src := fixtureRemote(t)
	writeAndCommit(t, src, "b.txt", "b\n", "second")
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if _, err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	h := history.New(db)
	rec := &recordingIndex{}
	p := NewPoller(PollerDeps{
		State: s, Git: gitrepo.New(gitBin, t.TempDir()), Index: rec.fn, History: h,
	})

	// When: the first, full poll.
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce() err = %v", err)
	}

	// Then: both commits are held.
	n, _, err := h.Count(ctx, "fixture")
	if err != nil || n != 2 {
		t.Fatalf("Count after full = %d, %v; want 2", n, err)
	}

	// When: a push, then an incremental poll.
	writeAndCommit(t, src, "c.txt", "c\n", "third: the new one")
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("second PollOnce() err = %v", err)
	}

	// Then: three, and the new one is searchable by its subject.
	got, err := h.Search(ctx, history.Query{Repos: []string{"fixture"}, Since: time.Time{}, Topic: "third"})
	if err != nil || len(got) != 1 || got[0].Paths[0] != "c.txt" {
		t.Errorf("Search = %+v, %v", got, err)
	}
	n, _, _ = h.Count(ctx, "fixture")
	if n != 3 {
		t.Errorf("Count after incremental = %d, want 3", n)
	}

	// When: the last commit is amended upstream (a rewritten history).
	gitRun(t, src, "commit", "-q", "--amend", "-m", "third: amended")
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("third PollOnce() err = %v", err)
	}
	// Then: the amended commit replaces the original, never sits beside it.
	n, _, _ = h.Count(ctx, "fixture")
	amended, _ := h.Search(ctx, history.Query{Repos: []string{"fixture"}, Since: time.Time{}, Topic: "third"})
	if n != 3 || len(amended) != 1 || amended[0].Subject != "third: amended" {
		t.Errorf("after the amend: %d commits, third = %+v", n, amended)
	}

	// When: the repository is reset (a clone_url mismatch, a migration).
	if err := s.ResetRepo(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	// Then: its history is gone with the files.
	n, _, _ = h.Count(ctx, "fixture")
	if n != 0 {
		t.Errorf("Count after reset = %d, want 0", n)
	}
}

func TestPollOnce_withoutTheLaneRecordsNothing(t *testing.T) {
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
		t.Fatalf("PollOnce() err = %v", err)
	}
	n, _, _ := history.New(db).Count(ctx, "fixture")
	if n != 0 {
		t.Errorf("a poller without History recorded %d commits", n)
	}
}
