package threads

import (
	"context"
	"strings"
	"testing"
)

func TestSaveFollowups_survivesAReload(t *testing.T) {
	// The pills are part of the record: a reader coming back to the thread is
	// offered what they were offered then, not a fresh set of guesses.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How does shipping work?", 0)
	if err := s.Finish(ctx, m.ID, "It ships.", nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	want := []string{"What happens on a partial shipment?", "Where is the carrier chosen?"}
	if err := s.SaveFollowups(ctx, m.ID, want); err != nil {
		t.Fatalf("SaveFollowups: %v", err)
	}

	list, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if got := list[0].Followups; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Followups = %q, want %q", got, want)
	}
	one, _, err := s.Message(ctx, "anna", m.ID)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if strings.Join(one.Followups, "|") != strings.Join(want, "|") {
		t.Errorf("Message().Followups = %q, want %q", one.Followups, want)
	}
}

func TestSaveFollowups_nothingToSaveWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)

	if err := s.SaveFollowups(ctx, m.ID, nil); err != nil {
		t.Fatalf("SaveFollowups: %v", err)
	}

	list, _ := s.Messages(ctx, "anna", th.ID)
	if len(list[0].Followups) != 0 {
		t.Errorf("Followups = %q, want none", list[0].Followups)
	}
}

func TestSaveFollowups_unreadableJSONCostsThePillsNotTheMessage(t *testing.T) {
	// A row whose provenance cannot be read is still a message, the same rule
	// scanScope follows.
	ctx := context.Background()
	db := threadDB(t)
	s := NewStore(db)
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)
	if _, err := db.ExecContext(ctx, `UPDATE messages SET followups = ? WHERE id = ?`, "{not json", m.ID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	list, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(list) != 1 || len(list[0].Followups) != 0 {
		t.Errorf("Followups = %q, want none and the message intact", list[0].Followups)
	}
}
