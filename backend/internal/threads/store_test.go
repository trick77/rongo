package threads

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/store"
)

func threadDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, s := range []string{"anna", "bruno"} {
		if _, err := db.Exec(`INSERT INTO users (subject, email, is_admin) VALUES (?, ?, 0)`, s, s+"@x.invalid"); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	return db
}

func TestCreate_titleStartsAsTheQuestionSoTheSidebarNeverWaits(t *testing.T) {
	s := NewStore(threadDB(t))

	got, err := s.Create(context.Background(), "anna", "Wie wird die Teaser-Mail verschickt?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got.Title == "" {
		t.Fatal("empty title; the sidebar entry must be there the moment the question is sent")
	}
	if !strings.HasPrefix(got.Title, "Wie wird die Teaser-Mail") {
		t.Errorf("title = %q, want the first words of the question", got.Title)
	}
}

func TestSetTitle_anEmptyModelTitleLeavesThePlaceholderStanding(t *testing.T) {
	// A title call that returns nothing is not a failure anyone needs to see.
	// Overwriting the placeholder with "" would blank the sidebar entry.
	s := NewStore(threadDB(t))
	th, _ := s.Create(context.Background(), "anna", "Wie laeuft der Versand?")

	if err := s.SetTitle(context.Background(), th.ID, "   "); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	list, _ := s.List(context.Background(), "anna")
	if list[0].Title == "" {
		t.Error("the placeholder was overwritten with an empty title")
	}
}

func TestFinish_storesTheAnswerWithItsEvidence(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "Wie?")
	m, err := s.AddQuestion(ctx, th.ID, "ba", "Wie?")
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}

	err = s.Finish(ctx, m.ID, "So laeuft es [1].", []ask.Citation{
		{Marker: 1, Repo: "peeq", Branch: "master", Path: "a.go", StartLine: 1, EndLine: 9},
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	msgs, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Answer != "So laeuft es [1]." {
		t.Fatalf("messages = %+v", msgs)
	}
	if len(msgs[0].Citations) != 1 || msgs[0].Citations[0].Branch != "master" {
		t.Errorf("citations = %+v, want the branch kept — a forge URL without it may 404", msgs[0].Citations)
	}
}

func TestFail_keepsTheQuestionInTheRecord(t *testing.T) {
	// A turn that broke still happened. Dropping the question would leave the
	// reader wondering what they asked.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "Wie?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "Wie?")

	if err := s.Fail(ctx, m.ID, "das Modell antwortete nicht"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	msgs, _ := s.Messages(ctx, "anna", th.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want the failed turn kept", msgs)
	}
	if msgs[0].Question != "Wie?" || msgs[0].Error == "" {
		t.Errorf("message = %+v, want the question plus the failure", msgs[0])
	}
}

func TestMessages_anotherUsersThreadIsNotReadable(t *testing.T) {
	// The subject is part of the query, not a check afterwards: a slip here
	// hands over someone else's conversation.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "Wie?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "Wie?")
	_ = s.Finish(ctx, m.ID, "geheim", nil)

	got, err := s.Messages(ctx, "bruno", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("bruno read anna's thread: %+v", got)
	}
	owns, err := s.Owns(ctx, "bruno", th.ID)
	if err != nil {
		t.Fatalf("Owns: %v", err)
	}
	if owns {
		t.Error("Owns said bruno owns anna's thread")
	}
}

func TestAddQuestion_appendsRatherThanRewrites(t *testing.T) {
	// The thread is a record. A follow-up adds a turn; nothing replaces one.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "Erste Frage?")
	first, _ := s.AddQuestion(ctx, th.ID, "ba", "Erste Frage?")
	_ = s.Finish(ctx, first.ID, "Erste Antwort.", nil)
	second, err := s.AddQuestion(ctx, th.ID, "dev", "Und als Dev?")
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	_ = s.Finish(ctx, second.ID, "Zweite Antwort.", nil)

	msgs, _ := s.Messages(ctx, "anna", th.ID)

	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want both turns", len(msgs))
	}
	if msgs[0].Answer != "Erste Antwort." {
		t.Errorf("the first answer changed: %q", msgs[0].Answer)
	}
	if msgs[0].Audience != "ba" || msgs[1].Audience != "dev" {
		t.Errorf("audiences = %q/%q, want them per message", msgs[0].Audience, msgs[1].Audience)
	}
}
