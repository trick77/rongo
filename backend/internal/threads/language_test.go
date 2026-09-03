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
