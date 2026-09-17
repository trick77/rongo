package threads

import (
	"database/sql"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
)

func insertCommit(t *testing.T, db *sql.DB, id int64, repo, sha, at, subject, paths string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO repo_state (name, clone_url, branch) VALUES (?, 'x', 'master')`, repo); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO commits (id, repo, sha, committed_at, author, subject, body, paths)
		VALUES (?, ?, ?, ?, 'someone', ?, 'why', ?)`, id, repo, sha, at, subject, paths); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
}

func TestCommitSourcesComeBackBesideChunks_andCarryNoAuthor(t *testing.T) {
	s, ctx, threadID, db := newThreadStore(t)
	msg, _ := s.AddQuestion(ctx, threadID, "ba", "en", "what changed?", 0)
	insertChunk(t, db, 1, "rongo", "a.go", "package a")
	insertCommit(t, db, 11, "rongo", "aaa1111", "2026-09-17T10:00:00Z", "Newest", "a.go\nb.go")
	insertCommit(t, db, 12, "rongo", "bbb2222", "2026-09-16T10:00:00Z", "Older", "c.go")
	if err := s.SaveSources(ctx, msg.ID, []ask.Source{
		{Kind: ask.SourceCommit, CommitID: 12, Reason: "hit"},
		{Kind: ask.SourceCommit, CommitID: 11, Reason: "hit"},
		{ChunkID: 1, Reason: "hit"},
	}); err != nil {
		t.Fatalf("save sources: %v", err)
	}

	got, total, err := s.Sources(ctx, testSubject, msg.ID)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if total != 3 || len(got) != 3 {
		t.Fatalf("total %d, got %d, want 3 and 3", total, len(got))
	}
	// Chunks first, then commits newest first.
	if got[0].ChunkID != 1 || got[1].CommitID != 11 || got[2].CommitID != 12 {
		t.Errorf("order = %+v", got)
	}
	c := got[1]
	if !c.IsCommit() || c.Repo != "rongo" || c.Branch != "master" || c.SHA != "aaa1111" || c.Subject != "Newest" ||
		c.Text != "why" || len(c.Paths) != 2 || c.Paths[1] != "b.go" ||
		!c.CommittedAt.Equal(time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("commit source = %+v", c)
	}

	// A commit a reset removed is simply missing, like a chunk.
	if _, err := db.Exec(`DELETE FROM commits WHERE id = 12`); err != nil {
		t.Fatal(err)
	}
	got, total, _ = s.Sources(ctx, testSubject, msg.ID)
	if total != 3 || len(got) != 2 {
		t.Errorf("after the reset: total %d, got %d", total, len(got))
	}
	// Not the owner: nothing.
	other, _, _ := s.Sources(ctx, "someone-else", msg.ID)
	if len(other) != 0 {
		t.Errorf("a foreign subject read %d sources", len(other))
	}
}

func TestCommitCitationsRoundTrip_andTheShareServesOnlyThem(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answeredTurn(t, s, th, "What changed?", "This [1] and that [2].",
		ask.Citation{Marker: 1, Repo: "rongo", Branch: "master", SHA: "aaa1111", Kind: ask.SourceCommit,
			Subject: "Newest", CommittedAt: "2026-09-17T10:00:00Z"},
		ask.Citation{Marker: 2, Repo: "rongo", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 2, SHA: "deadbeef"})

	msgs, err := s.Messages(ctx, testSubject, th)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages: %v", err)
	}
	c := msgs[0].Citations[0]
	if c.Kind != ask.SourceCommit || c.Subject != "Newest" || c.CommittedAt != "2026-09-17T10:00:00Z" || c.SHA != "aaa1111" {
		t.Errorf("commit citation = %+v", c)
	}
	if f := msgs[0].Citations[1]; f.Kind != "" || f.Subject != "" {
		t.Errorf("file citation grew commit fields: %+v", f)
	}

	sh, err := s.Share(ctx, testSubject, th)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if ok, _ := s.SharedCommit(ctx, sh.Token, "rongo", "aaa1111"); !ok {
		t.Error("the cited commit is not served through the link")
	}
	if ok, _ := s.SharedCommit(ctx, sh.Token, "rongo", "deadbeef"); ok {
		t.Error("a file citation's commit opens as a commit view")
	}
	if ok, _ := s.SharedCitation(ctx, sh.Token, "rongo", "", "aaa1111"); ok {
		t.Error("a commit citation opens as a file")
	}
}
