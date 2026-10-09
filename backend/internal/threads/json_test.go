package threads

import (
	"context"
	"testing"
)

func TestDecodeJSON_emptyAndUnreadableAreTheZeroValue(t *testing.T) {
	if got := decodeJSON[[]string](`["a","b"]`); len(got) != 2 || got[0] != "a" {
		t.Fatalf("decodeJSON(list) = %v", got)
	}
	if got := decodeJSON[[]string](""); got != nil {
		t.Fatalf("decodeJSON(empty) = %v, want nil", got)
	}
	if got := decodeJSON[[]string]("{not json"); got != nil {
		t.Fatalf("decodeJSON(garbage) = %v, want nil", got)
	}
	type sc struct{ Known []string }
	if got := decodeJSON[sc]("{not json"); got.Known != nil {
		t.Fatalf("decodeJSON(struct garbage) = %+v, want zero", got)
	}
}

func TestSetJSON_refusesAColumnOffTheList(t *testing.T) {
	// The column is spliced into the statement, so only the four names the
	// callers spell out may reach it; a typo must fail in a test, never
	// become an UPDATE of some other column.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)

	if err := s.setJSON(ctx, "answer", m.ID, "x"); err == nil {
		t.Fatal("setJSON(answer) = nil, want a refusal")
	}
	if err := s.setJSON(ctx, "followups", m.ID, []string{"q"}); err != nil {
		t.Fatalf("setJSON(followups) = %v", err)
	}
	list, _ := s.Messages(ctx, "anna", th.ID)
	if len(list) != 1 || len(list[0].Followups) != 1 || list[0].Followups[0] != "q" {
		t.Fatalf("Followups = %q", list[0].Followups)
	}
}
