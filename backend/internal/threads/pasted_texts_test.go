package threads

import (
	"context"
	"testing"
)

func TestSavePastedTexts_survivesAReload(t *testing.T) {
	// A pasted block is drawn as a chip, not as prose: the record must say
	// which trailing part of the question was a paste, or a reload shows the
	// stack trace as the reader's own words.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "Why does this fail?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "Why does this fail?\n\npanic: boom\nmain.go:12", 0)

	want := []PastedText{{Text: "panic: boom\nmain.go:12", Lines: 2}}
	if err := s.SavePastedTexts(ctx, m.ID, want); err != nil {
		t.Fatalf("SavePastedTexts: %v", err)
	}

	list, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if got := list[0].PastedTexts; len(got) != 1 || got[0] != want[0] {
		t.Errorf("PastedTexts = %+v, want %+v", got, want)
	}
	one, _, err := s.Message(ctx, "anna", m.ID)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if got := one.PastedTexts; len(got) != 1 || got[0] != want[0] {
		t.Errorf("Message().PastedTexts = %+v, want %+v", got, want)
	}
}

func TestSavePastedTexts_nothingToSaveWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)

	if err := s.SavePastedTexts(ctx, m.ID, nil); err != nil {
		t.Fatalf("SavePastedTexts: %v", err)
	}

	list, _ := s.Messages(ctx, "anna", th.ID)
	if len(list[0].PastedTexts) != 0 {
		t.Errorf("PastedTexts = %+v, want none", list[0].PastedTexts)
	}
}

func TestSavePastedTexts_unreadableJSONCostsTheChipsNotTheMessage(t *testing.T) {
	ctx := context.Background()
	db := threadDB(t)
	s := NewStore(db)
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)
	for _, blob := range []string{"", "[]", "{not json"} {
		if _, err := db.ExecContext(ctx, `UPDATE messages SET pasted_texts = ? WHERE id = ?`, blob, m.ID); err != nil {
			t.Fatalf("seed: %v", err)
		}
		list, err := s.Messages(ctx, "anna", th.ID)
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(list) != 1 || len(list[0].PastedTexts) != 0 {
			t.Errorf("blob %q: PastedTexts = %+v, want none and the message intact", blob, list[0].PastedTexts)
		}
	}
}
