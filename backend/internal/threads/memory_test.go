package threads

import (
	"math"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

// TestLastTurnSkipsATurnThatWasOnlyAnInstruction: "never show flowcharts"
// answered, templated, but nothing follows it. Handed over as the antecedent
// it would make "kürzer" typed after it search for "kürzer" instead of
// reworking the answer before it.
func TestLastTurnSkipsATurnThatWasOnlyAnInstruction(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)
	answered, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does rongo cite sources?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Finish(ctx, answered.ID, "Every claim carries repo, file and line.", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	rule, err := s.AddQuestion(ctx, threadID, "ba", "en", "Never show me flowcharts.", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.SetScope(ctx, rule.ID, ask.Scope{Intent: ask.IntentMemory}); err != nil {
		t.Fatalf("set scope: %v", err)
	}
	if err := s.Finish(ctx, rule.ID, `Noted: "Never draw flowchart diagrams."`, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	got, ok, err := s.LastTurnBefore(ctx, testSubject, threadID, math.MaxInt)
	if err != nil || !ok {
		t.Fatalf("last turn: %v ok=%v", err, ok)
	}
	if got.ID != answered.ID {
		t.Errorf("last turn = %d, want the answered one (%d), not the instruction (%d)", got.ID, answered.ID, rule.ID)
	}
}

// TestMessagesCarryTheRuleATurnSavedUntilItIsDeleted: the chip and its undo
// come off the memories row the turn points at, so a deleted rule leaves no
// chip behind.
func TestMessagesCarryTheRuleATurnSavedUntilItIsDeleted(t *testing.T) {
	s, ctx, threadID, db := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "Never show me flowcharts.", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	res, err := db.Exec(`INSERT INTO memories (user_subject, text) VALUES (?, ?)`, testSubject, "Never draw flowchart diagrams.")
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	memID, _ := res.LastInsertId()
	if err := s.SetMemory(ctx, msg.ID, memID); err != nil {
		t.Fatalf("set memory: %v", err)
	}

	msgs, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Memory == nil || msgs[0].Memory.ID != memID || msgs[0].Memory.Text != "Never draw flowchart diagrams." {
		t.Fatalf("messages = %+v", msgs)
	}

	if _, err := db.Exec(`DELETE FROM memories WHERE id = ?`, memID); err != nil {
		t.Fatalf("delete memory: %v", err)
	}
	msgs, _ = s.Messages(ctx, testSubject, threadID)
	if msgs[0].Memory != nil {
		t.Fatalf("a deleted rule still shows: %+v", msgs[0].Memory)
	}
}

// TestMessagesSayWhichTurnsHaveNoSources: the page hides the re-explain
// action on a turn that answered from none, and the record is what says so.
func TestMessagesSayWhichTurnsHaveNoSources(t *testing.T) {
	s, ctx, threadID, db := newThreadStore(t)
	none, err := s.AddQuestion(ctx, threadID, "ba", "en", "Anything about warp drives?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Finish(ctx, none.ID, "I found nothing about this in the indexed code.", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	some, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does rongo cite sources?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Finish(ctx, some.ID, "Every claim carries a marker.", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO message_sources (message_id, chunk_id, reason, hop) VALUES (?, 0, 'hit', 0)`, some.ID); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	msgs, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 2 || !msgs[0].Sourceless || msgs[1].Sourceless {
		t.Fatalf("sourceless = %v, %v; want the nothing-found turn only", msgs[0].Sourceless, msgs[1].Sourceless)
	}
}
