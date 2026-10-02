package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Feedback is the reader's verdict on a thread. It is kept for reading later
// and nothing else: no prompt, memory block or shared page ever sees it.
type Feedback struct {
	// Verdict is 1 for helpful, -1 for not.
	Verdict int `json:"verdict"`
	// Reason is one of feedbackReasons; empty when none was given.
	Reason string `json:"reason"`
	// UpToMessageID is the newest finished answer the verdict covered. Turns
	// asked after it are not part of the verdict until the reader gives it
	// again.
	UpToMessageID int64 `json:"upToMessageId"`
}

// feedbackReasons is the closed set the schema's CHECK holds too.
var feedbackReasons = map[string]bool{
	"": true, "wrong": true, "incomplete": true, "missed_code": true, "too_long": true, "wrong_repo": true,
}

// ValidFeedbackReason reports whether r is one of the reasons a verdict may carry.
func ValidFeedbackReason(r string) bool { return feedbackReasons[r] }

// SetFeedback records the reader's verdict, replacing any earlier one, and
// pins it to the thread's newest finished answer. Reports false for a thread
// that is not this reader's or holds no finished answer: there is nothing of
// theirs to judge, and the edge answers 404 to both.
func (s *Store) SetFeedback(ctx context.Context, subject string, threadID int64, verdict int, reason string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO thread_feedback (thread_id, verdict, reason, up_to_message_id, updated_at)
		SELECT t.id, ?, ?, MAX(m.id), datetime('now')
		  FROM threads t JOIN messages m ON m.thread_id = t.id
		 WHERE t.id = ? AND t.user_subject = ? AND m.answer != '' AND m.error = ''
		 GROUP BY t.id
		ON CONFLICT (thread_id) DO UPDATE SET
		   verdict = excluded.verdict,
		   reason = excluded.reason,
		   up_to_message_id = excluded.up_to_message_id,
		   updated_at = excluded.updated_at`,
		verdict, reason, threadID, subject)
	if err != nil {
		return false, fmt.Errorf("set feedback: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set feedback: %w", err)
	}
	return n > 0, nil
}

// ClearFeedback takes the verdict away. Reports false only for a thread that
// is not this reader's; clearing a verdict that was never given still matched.
func (s *Store) ClearFeedback(ctx context.Context, subject string, threadID int64) (bool, error) {
	owns, err := s.Owns(ctx, subject, threadID)
	if err != nil || !owns {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM thread_feedback WHERE thread_id = ?`, threadID); err != nil {
		return false, fmt.Errorf("clear feedback: %w", err)
	}
	return true, nil
}

// Feedback reads the reader's verdict on their own thread. found is false when
// none was given or the thread is not theirs.
func (s *Store) Feedback(ctx context.Context, subject string, threadID int64) (Feedback, bool, error) {
	var f Feedback
	err := s.db.QueryRowContext(ctx, `
		SELECT f.verdict, f.reason, f.up_to_message_id
		  FROM thread_feedback f JOIN threads t ON t.id = f.thread_id
		 WHERE f.thread_id = ? AND t.user_subject = ?`, threadID, subject).
		Scan(&f.Verdict, &f.Reason, &f.UpToMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return Feedback{}, false, nil
	}
	if err != nil {
		return Feedback{}, false, fmt.Errorf("read feedback: %w", err)
	}
	return f, true, nil
}
