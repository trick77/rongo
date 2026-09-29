package threads

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/sourceview"
)

// fakeEvidence is the checkout as Sources reads it: files by (repo, path,
// sha), commits by (repo, sha). What it lacks is not found, the way a purged
// repository or a replaced snapshot is.
type fakeEvidence struct {
	files   map[string]string
	commits map[string]sourceview.Commit
	reads   int
}

func (f *fakeEvidence) Read(_ context.Context, repo, path, sha string) (sourceview.File, error) {
	f.reads++
	body, ok := f.files[repo+"|"+path+"|"+sha]
	if !ok {
		return sourceview.File{}, fmt.Errorf("%w: %s/%s@%s", sourceview.ErrNotFound, repo, path, sha)
	}
	return sourceview.File{Repo: repo, Branch: "master", Path: path, SHA: sha, Content: body}, nil
}

func (f *fakeEvidence) RecordedCommit(_ context.Context, repo, sha string) (sourceview.Commit, error) {
	c, ok := f.commits[repo+"|"+sha]
	if !ok {
		return sourceview.Commit{}, fmt.Errorf("%w: %s@%s", sourceview.ErrNotFound, repo, sha)
	}
	return c, nil
}

func reindexFile(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	// What ReplaceFile does to a touched file: every chunk goes, new ids come.
	if _, err := db.Exec(`DELETE FROM chunks WHERE file_id IN (SELECT id FROM files WHERE path = ?)`, path); err != nil {
		t.Fatalf("re-index %s: %v", path, err)
	}
}

func TestSourcesSurviveARe_indexByReadingTheCommitTheyWereReadFrom(t *testing.T) {
	// Given an answer written from two windows of one file, then a poll that
	// re-indexed the file and so gave every chunk of it a new id
	s, ctx, threadID, db := newThreadStore(t)
	ev := &fakeEvidence{files: map[string]string{
		"peeq|a.go|deadbeef": "package a\n\nfunc A() {}\nfunc B() {}\n",
	}}
	s.WithEvidence(ev)
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "de", "frage", 0)
	insertChunk(t, db, 1, "peeq", "a.go", "package a")
	if err := s.SaveSources(ctx, msg.ID, []ask.Source{
		{ChunkID: 1, Repo: "peeq", Path: "a.go", SHA: "deadbeef", StartLine: 1, EndLine: 1, Reason: "hit"},
		{ChunkID: 2, Repo: "peeq", Path: "a.go", SHA: "deadbeef", StartLine: 3, EndLine: 4, Symbol: "A", Reason: "reference:A", Hop: 1},
	}); err != nil {
		t.Fatalf("save sources: %v", err)
	}
	reindexFile(t, db, "a.go")

	// When
	got, total, err := s.Sources(ctx, testSubject, msg.ID)

	// Then the basis is whole, in the text it had when the answer was written
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("total %d, got %d, want 2 and 2", total, len(got))
	}
	if got[0].Text != "package a" || got[1].Text != "func A() {}\nfunc B() {}" {
		t.Errorf("texts = %q, %q", got[0].Text, got[1].Text)
	}
	if got[1].Symbol != "A" || got[1].Reason != "reference:A" || got[1].Hop != 1 || got[1].Branch != "master" || got[1].SHA != "deadbeef" {
		t.Errorf("second source = %+v", got[1])
	}
	if ev.reads != 1 {
		t.Errorf("read the file %d times, want once for both windows", ev.reads)
	}
}

func TestASourceGitCannotProduceIsMissing(t *testing.T) {
	// A purged repository, a replaced snapshot: the object is gone, and the
	// basis is short by it. Rework refuses on that; it must not be hidden.
	s, ctx, threadID, _ := newThreadStore(t)
	s.WithEvidence(&fakeEvidence{files: map[string]string{"peeq|a.go|deadbeef": "package a\n"}})
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "en", "q", 0)
	if err := s.SaveSources(ctx, msg.ID, []ask.Source{
		{ChunkID: 1, Repo: "peeq", Path: "a.go", SHA: "deadbeef", StartLine: 1, EndLine: 1, Reason: "hit"},
		{ChunkID: 2, Repo: "gone", Path: "b.go", SHA: "cafe", StartLine: 1, EndLine: 2, Reason: "hit"},
	}); err != nil {
		t.Fatalf("save sources: %v", err)
	}

	got, total, err := s.Sources(ctx, testSubject, msg.ID)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if total != 2 || len(got) != 1 || got[0].Path != "a.go" {
		t.Errorf("total %d, got %+v; want 2 and only a.go", total, got)
	}
}

func TestSplitSiblingsOfOneLineAreOneSource(t *testing.T) {
	// An overlong line is stored as sibling chunks sharing one line range.
	// Read back from git they are the same text; counting them twice would
	// report a basis short by one that is in fact whole.
	s, ctx, threadID, _ := newThreadStore(t)
	s.WithEvidence(&fakeEvidence{files: map[string]string{"peeq|min.js|deadbeef": "x=1\n"}})
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "en", "q", 0)
	if err := s.SaveSources(ctx, msg.ID, []ask.Source{
		{ChunkID: 1, Repo: "peeq", Path: "min.js", SHA: "deadbeef", StartLine: 1, EndLine: 1, Reason: "hit"},
		{ChunkID: 2, Repo: "peeq", Path: "min.js", SHA: "deadbeef", StartLine: 1, EndLine: 1, Reason: "hit"},
	}); err != nil {
		t.Fatalf("save sources: %v", err)
	}

	got, total, err := s.Sources(ctx, testSubject, msg.ID)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].Text != "x=1" {
		t.Errorf("total %d, got %+v; want one source", total, got)
	}
}

func TestACommitSourceOutlivesItsRowInTheCommitLane(t *testing.T) {
	// The window slides with every push, and dropAbsent deletes what fell
	// out of it. The commit is still in git; the record reads it there.
	s, ctx, threadID, db := newThreadStore(t)
	at := "2026-09-17T10:00:00Z"
	s.WithEvidence(&fakeEvidence{commits: map[string]sourceview.Commit{
		"rongo|aaa1111": {Repo: "rongo", Branch: "master", SHA: "aaa1111", CommittedAt: at, Subject: "Newest", Body: "why",
			Files: []sourceview.FileChange{{Path: "a.go"}, {Path: "b.go"}}},
	}})
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "en", "what changed?", 0)
	insertCommit(t, db, 11, "rongo", "aaa1111", at, "Newest", "a.go\nb.go")
	if err := s.SaveSources(ctx, msg.ID, []ask.Source{
		{Kind: ask.SourceCommit, CommitID: 11, Repo: "rongo", SHA: "aaa1111", Reason: "hit"},
	}); err != nil {
		t.Fatalf("save sources: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM commits WHERE id = 11`); err != nil {
		t.Fatal(err)
	}

	got, total, err := s.Sources(ctx, testSubject, msg.ID)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("total %d, got %d", total, len(got))
	}
	c := got[0]
	if !c.IsCommit() || c.Subject != "Newest" || c.Text != "why" || len(c.Paths) != 2 ||
		!c.CommittedAt.Equal(time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("commit source = %+v", c)
	}
}

func TestARowFromBeforeTheIdentityStillResolvesByItsChunk(t *testing.T) {
	// A row the backfill could not complete has only its chunk id; it reads
	// the way every row did before, so an older thread keeps working.
	s, ctx, threadID, db := newThreadStore(t)
	s.WithEvidence(&fakeEvidence{})
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "en", "q", 0)
	insertChunk(t, db, 1, "peeq", "a.go", "package a")
	if err := s.SaveSources(ctx, msg.ID, []ask.Source{{ChunkID: 1, Reason: "hit"}}); err != nil {
		t.Fatalf("save sources: %v", err)
	}

	got, total, err := s.Sources(ctx, testSubject, msg.ID)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].Text != "package a" {
		t.Errorf("total %d, got %+v", total, got)
	}
}
