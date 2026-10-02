package threads

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

// answered adds a finished turn to the thread and hands back its row id.
func answered(ctx context.Context, t *testing.T, s *Store, threadID int64) int64 {
	t.Helper()
	m, err := s.AddQuestion(ctx, threadID, "ba", "en", "How?", 0)
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if err := s.Finish(ctx, m.ID, "Like so.", []ask.Citation{{Marker: 1, Repo: "r", Path: "a.go", StartLine: 1, EndLine: 2}}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return m.ID
}

func TestSetFeedback_aSecondVerdictReplacesTheFirst(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answered(ctx, t, s, th)

	if ok, err := s.SetFeedback(ctx, testSubject, th, 1, ""); err != nil || !ok {
		t.Fatalf("SetFeedback up: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SetFeedback(ctx, testSubject, th, -1, "incomplete"); err != nil || !ok {
		t.Fatalf("SetFeedback down: ok=%v err=%v", ok, err)
	}

	fb, found, err := s.Feedback(ctx, testSubject, th)
	if err != nil || !found {
		t.Fatalf("Feedback: found=%v err=%v", found, err)
	}
	if fb.Verdict != -1 || fb.Reason != "incomplete" {
		t.Errorf("feedback = %+v, want the second verdict", fb)
	}
}

func TestSetFeedback_anotherReadersThreadIsNotFound(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answered(ctx, t, s, th)

	ok, err := s.SetFeedback(ctx, "bob", th, 1, "")
	if err != nil {
		t.Fatalf("SetFeedback: %v", err)
	}
	if ok {
		t.Error("bob rated anna's thread")
	}
	if _, found, _ := s.Feedback(ctx, "bob", th); found {
		t.Error("bob read anna's feedback")
	}
}

// A thread holding only a failure or a waiting card has no answer to judge.
func TestSetFeedback_aThreadWithNoFinishedAnswerIsNotFound(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	m, _ := s.AddQuestion(ctx, th, "ba", "en", "How?", 0)
	if err := s.Fail(ctx, m.ID, "boom"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	ok, err := s.SetFeedback(ctx, testSubject, th, 1, "")
	if err != nil {
		t.Fatalf("SetFeedback: %v", err)
	}
	if ok {
		t.Error("rated a thread with no finished answer")
	}
}

// The verdict covers the turns that were there when it was given; the next
// one moves it along.
func TestSetFeedback_coversTheNewestFinishedAnswer(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	first := answered(ctx, t, s, th)
	if _, err := s.SetFeedback(ctx, testSubject, th, 1, ""); err != nil {
		t.Fatalf("SetFeedback: %v", err)
	}
	second := answered(ctx, t, s, th)
	failed, _ := s.AddQuestion(ctx, th, "ba", "en", "And?", 0)
	_ = s.Fail(ctx, failed.ID, "boom")

	fb, _, _ := s.Feedback(ctx, testSubject, th)
	if fb.UpToMessageID != first {
		t.Errorf("up to = %d, want %d — a later turn must not move a verdict nobody re-gave", fb.UpToMessageID, first)
	}

	if _, err := s.SetFeedback(ctx, testSubject, th, 1, ""); err != nil {
		t.Fatalf("SetFeedback: %v", err)
	}
	fb, _, _ = s.Feedback(ctx, testSubject, th)
	if fb.UpToMessageID != second {
		t.Errorf("up to = %d, want %d, the newest finished answer and not the failure after it", fb.UpToMessageID, second)
	}
}

func TestClearFeedback_takesTheVerdictAway(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	answered(ctx, t, s, th)
	_, _ = s.SetFeedback(ctx, testSubject, th, 1, "")

	if ok, err := s.ClearFeedback(ctx, "bob", th); err != nil || ok {
		t.Fatalf("bob cleared anna's verdict: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ClearFeedback(ctx, testSubject, th); err != nil || !ok {
		t.Fatalf("ClearFeedback: ok=%v err=%v", ok, err)
	}
	if _, found, _ := s.Feedback(ctx, testSubject, th); found {
		t.Error("verdict still there after clearing")
	}
	// Clearing what is not there is the same outcome, not an error.
	if ok, err := s.ClearFeedback(ctx, testSubject, th); err != nil || !ok {
		t.Errorf("second clear: ok=%v err=%v", ok, err)
	}
}

func TestDelete_takesTheFeedbackWithTheThread(t *testing.T) {
	s, ctx, th, db := newThreadStore(t)
	answered(ctx, t, s, th)
	_, _ = s.SetFeedback(ctx, testSubject, th, -1, "wrong")

	if _, err := s.Delete(ctx, testSubject, th); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thread_feedback`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("feedback rows = %d, want the cascade to have taken them", n)
	}
}

func TestFeedback_aDatabaseThatCannotAnswerIsAnError(t *testing.T) {
	s, ctx, th, db := newThreadStore(t)
	answered(ctx, t, s, th)
	_ = db.Close()

	if _, err := s.SetFeedback(ctx, testSubject, th, 1, ""); err == nil {
		t.Error("SetFeedback: no error")
	}
	if _, err := s.ClearFeedback(ctx, testSubject, th); err == nil {
		t.Error("ClearFeedback: no error")
	}
	if _, _, err := s.Feedback(ctx, testSubject, th); err == nil {
		t.Error("Feedback: no error")
	}
}

func TestFeedbackReasons_isTheClosedSet(t *testing.T) {
	for _, r := range []string{"", "wrong", "incomplete", "missed_code", "too_long", "wrong_repo"} {
		if !ValidFeedbackReason(r) {
			t.Errorf("%q refused", r)
		}
	}
	for _, r := range []string{"Wrong", "other", "too long"} {
		if ValidFeedbackReason(r) {
			t.Errorf("%q accepted", r)
		}
	}
}
