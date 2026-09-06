package threads

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/timeline"
)

func TestSaveSteps_survivesAReload(t *testing.T) {
	// The timeline is part of the record the reader watched. Without it a
	// thread reopened a minute later says less about the turn than the turn
	// itself did while it ran.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How does shipping work?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How does shipping work?", 0)

	want := timeline.Trace{
		StartedAt: 1_700_000_000_000,
		EndedAt:   1_700_000_003_400,
		Steps: []timeline.Step{
			{Step: "understanding", At: 1_700_000_000_100},
			{Step: "writing", At: 1_700_000_001_900},
		},
	}
	if err := s.SaveSteps(ctx, m.ID, want); err != nil {
		t.Fatalf("SaveSteps: %v", err)
	}

	list, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	got := list[0].Steps
	if got == nil {
		t.Fatalf("Steps = nil, want the stored timeline")
	}
	// The turn's own span, not the gap between its first and last step: the
	// turn began before it announced anything and closed after the last step.
	if got.StartedAt != want.StartedAt || got.EndedAt != want.EndedAt {
		t.Errorf("span = %d..%d, want %d..%d", got.StartedAt, got.EndedAt, want.StartedAt, want.EndedAt)
	}
	if len(got.Steps) != 2 || got.Steps[0].Step != "understanding" || got.Steps[1].At != 1_700_000_001_900 {
		t.Errorf("Steps = %+v, want the two announced steps in order", got.Steps)
	}
}

func TestSaveSteps_aTurnThatAnnouncedNothingStoresNothing(t *testing.T) {
	// An empty frame under a question would be a claim about a turn nobody
	// watched, and it is what every turn older than the column looks like.
	ctx := context.Background()
	s := NewStore(threadDB(t))
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)

	if err := s.SaveSteps(ctx, m.ID, timeline.Trace{}); err != nil {
		t.Fatalf("SaveSteps: %v", err)
	}

	list, _ := s.Messages(ctx, "anna", th.ID)
	if list[0].Steps != nil {
		t.Errorf("Steps = %+v, want none", list[0].Steps)
	}
}

func TestSaveSteps_unreadableJSONCostsTheTraceNotTheMessage(t *testing.T) {
	// The same rule scanScope and scanFollowups follow: a row whose provenance
	// cannot be read is still a message.
	ctx := context.Background()
	db := threadDB(t)
	s := NewStore(db)
	th, _ := s.Create(ctx, "anna", "How?")
	m, _ := s.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)
	if _, err := db.ExecContext(ctx, `UPDATE messages SET steps = ? WHERE id = ?`, "{not json", m.ID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	list, err := s.Messages(ctx, "anna", th.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(list) != 1 || list[0].Steps != nil {
		t.Errorf("Steps = %+v, want none and the message intact", list[0].Steps)
	}
}
