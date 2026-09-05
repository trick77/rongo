package threads

import (
	"context"
	"testing"
)

func TestAddQuestion_storesTheLanguageWithTheTurn(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	m, err := s.AddQuestion(ctx, th.ID, "ba", "it", "How?")
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if m.Language != "it" {
		t.Errorf("returned language = %q, want it", m.Language)
	}

	msgs, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Language != "it" {
		t.Errorf("Messages language = %+v, want it", msgs)
	}
	one, found, err := s.Message(ctx, "anna", m.ID)
	if err != nil || !found || one.Language != "it" {
		t.Errorf("Message = %+v found=%v err=%v, want language it", one, found, err)
	}
}

// A thread is answered in one language: the one its first turn was asked in.
// A later turn asking for another one gets the thread's, so the record cannot
// hold two languages even when a stale tab sends the old composer value.
func TestAddQuestion_pinsTheLanguageToTheThreadsFirstTurn(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	if _, err := s.AddQuestion(ctx, th.ID, "ba", "de", "How?"); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}

	second, err := s.AddQuestion(ctx, th.ID, "ba", "fr", "And then?")
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if second.Language != "de" {
		t.Errorf("returned language = %q, want de — the thread's language", second.Language)
	}

	msgs, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 2 || msgs[1].Language != "de" {
		t.Errorf("stored languages = %+v, want both de", msgs)
	}
}
