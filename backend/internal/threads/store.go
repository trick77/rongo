// Package threads persists the record of a conversation.
//
// The thread is a RECORD, not a working document. A follow-up appends; nothing
// here rewrites an answer, and a turn that failed keeps its question. Someone
// reading a thread later has to see what was actually said at the time —
// including the wrong turn that led to the right question.
package threads

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/ask"
)

// Thread is one conversation.
type Thread struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// Message is one question and the answer it got.
type Message struct {
	ID        int64          `json:"id"`
	Ordinal   int            `json:"ordinal"`
	Audience  string         `json:"audience"`
	Question  string         `json:"question"`
	Answer    string         `json:"answer"`
	Error     string         `json:"error"`
	Citations []ask.Citation `json:"citations"`
	CreatedAt time.Time      `json:"created_at"`
}

// Store reads and writes threads.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// placeholderTitle is the first words of the question, shown in the sidebar the
// moment it is sent. The model writes a better one concurrently and replaces
// this quietly; the answer never waits for a title, and a title that never
// arrives is not an error anyone needs to see.
func placeholderTitle(question string) string {
	const max = 48
	q := strings.TrimSpace(strings.Join(strings.Fields(question), " "))
	if len([]rune(q)) <= max {
		return q
	}
	return string([]rune(q)[:max-1]) + "…"
}

// Create opens a thread for a question.
func (s *Store) Create(ctx context.Context, subject, question string) (Thread, error) {
	title := placeholderTitle(question)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO threads (user_subject, title) VALUES (?, ?)`, subject, title)
	if err != nil {
		return Thread{}, fmt.Errorf("create thread: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Thread{}, fmt.Errorf("create thread: %w", err)
	}
	return Thread{ID: id, Title: title, CreatedAt: time.Now().UTC()}, nil
}

// SetTitle replaces the placeholder. A failure is swallowed by the caller on
// purpose: a missing title is cosmetic, and it must never fail a turn.
func (s *Store) SetTitle(ctx context.Context, id int64, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE threads SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		return fmt.Errorf("set thread title: %w", err)
	}
	return nil
}

// AddQuestion appends a question and returns the message it created. The answer
// arrives later, through Finish.
func (s *Store) AddQuestion(ctx context.Context, threadID int64, audience, question string) (Message, error) {
	// The ordinal is computed inside the INSERT, not read and then written.
	// Two tabs submitting on one thread would otherwise compute the same number
	// and the second would hit UNIQUE (thread_id, ordinal) — a 500 for a thing
	// people do routinely.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (thread_id, ordinal, audience, question)
		VALUES (?, (SELECT COALESCE(MAX(ordinal), -1) + 1 FROM messages WHERE thread_id = ?), ?, ?)`,
		threadID, threadID, audience, question)
	if err != nil {
		return Message{}, fmt.Errorf("add question: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Message{}, fmt.Errorf("add question: %w", err)
	}
	var next int
	if err := s.db.QueryRowContext(ctx, `SELECT ordinal FROM messages WHERE id = ?`, id).Scan(&next); err != nil {
		return Message{}, fmt.Errorf("add question: %w", err)
	}
	return Message{ID: id, Ordinal: next, Audience: audience, Question: question}, nil
}

// Finish records the answer and its citations, in one transaction: an answer
// stored without its evidence is an answer nobody can check.
func (s *Store) Finish(ctx context.Context, messageID int64, answer string, citations []ask.Citation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE messages SET answer = ? WHERE id = ?`, answer, messageID); err != nil {
		return fmt.Errorf("store answer: %w", err)
	}
	for _, c := range citations {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO citations (message_id, marker, repo, branch, path, start_line, end_line)
			VALUES (?,?,?,?,?,?,?)`,
			messageID, c.Marker, c.Repo, c.Branch, c.Path, c.StartLine, c.EndLine); err != nil {
			return fmt.Errorf("store citation %d: %w", c.Marker, err)
		}
	}
	return tx.Commit()
}

// Fail records that a turn did not produce an answer. The question stays: a
// disappearing question leaves the reader wondering what they asked.
func (s *Store) Fail(ctx context.Context, messageID int64, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET error = ? WHERE id = ?`, msg, messageID)
	if err != nil {
		return fmt.Errorf("store turn failure: %w", err)
	}
	return nil
}

// List returns a user's threads, newest first.
func (s *Store) List(ctx context.Context, subject string) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, created_at FROM threads WHERE user_subject = ? ORDER BY id DESC`, subject)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	defer rows.Close()
	out := []Thread{}
	for rows.Next() {
		var t Thread
		var created string
		if err := rows.Scan(&t.ID, &t.Title, &created); err != nil {
			return nil, fmt.Errorf("scan thread: %w", err)
		}
		t.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Messages returns a thread's turns in order, with their citations.
func (s *Store) Messages(ctx context.Context, subject string, threadID int64) ([]Message, error) {
	// The subject is part of the query rather than checked afterwards: a thread
	// belongs to the person who asked, and a mistake here hands someone else's
	// conversation over.
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.ordinal, m.audience, m.question, m.answer, m.error, m.created_at
		FROM messages m JOIN threads t ON t.id = m.thread_id
		WHERE m.thread_id = ? AND t.user_subject = ?
		ORDER BY m.ordinal`, threadID, subject)
	if err != nil {
		return nil, fmt.Errorf("read thread: %w", err)
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.Ordinal, &m.Audience, &m.Question, &m.Answer, &m.Error, &created); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		cites, err := s.citations(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Citations = cites
	}
	return out, nil
}

func (s *Store) citations(ctx context.Context, messageID int64) ([]ask.Citation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT marker, repo, branch, path, start_line, end_line FROM citations
		 WHERE message_id = ? ORDER BY marker`, messageID)
	if err != nil {
		return nil, fmt.Errorf("read citations: %w", err)
	}
	defer rows.Close()
	out := []ask.Citation{}
	for rows.Next() {
		var c ask.Citation
		if err := rows.Scan(&c.Marker, &c.Repo, &c.Branch, &c.Path, &c.StartLine, &c.EndLine); err != nil {
			return nil, fmt.Errorf("scan citation: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Owns reports whether the thread belongs to this subject.
func (s *Store) Owns(ctx context.Context, subject string, threadID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM threads WHERE id = ? AND user_subject = ?`, threadID, subject).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check thread owner: %w", err)
	}
	return n > 0, nil
}
