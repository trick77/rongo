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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/usage"
)

// Thread is one conversation.
type Thread struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// Message is one question and the answer it got.
type Message struct {
	ID int64 `json:"id"`
	// ThreadID is the thread this message belongs to. Re-explaining reads it
	// off a bare message id to add the new turn to the same thread, the same
	// way a resumed clarification does.
	ThreadID int64  `json:"thread_id"`
	Ordinal  int    `json:"ordinal"`
	Audience string `json:"audience"`
	// Language is the language the answer was written in, per message like
	// Audience: the selector sits on the input field.
	Language  string         `json:"language"`
	Question  string         `json:"question"`
	Answer    string         `json:"answer"`
	Error     string         `json:"error"`
	Citations []ask.Citation `json:"citations"`
	// Clarification is set when this turn ended by asking which mechanism was
	// meant, so a reload renders the card instead of a turn that looks stuck.
	Clarification *Clarification `json:"clarification,omitempty"`
	// FromCandidateIdx is the candidate this turn resumed from, or -1 when it
	// did not resume a clarification.
	FromCandidateIdx int `json:"from_candidate_idx"`
	// FromClarificationID is the clarification this turn resumed from, or 0
	// when it did not resume one. It is what lets a reload mark the right
	// candidate on the right card: two clarifications can be open in one
	// thread at once, and position alone cannot tell them apart.
	FromClarificationID int64     `json:"from_clarification_id"`
	CreatedAt           time.Time `json:"created_at"`
	// Calls is every paid call this turn made, as stored. Tokens only: the
	// HTTP layer prices them into Usage, because the price table is
	// configuration the store does not know.
	Calls []usage.Call `json:"-"`
	// Usage is what the browser sees: the calls, their sum, and the cost
	// when prices are configured. Filled by the HTTP layer, never here.
	Usage *usage.Report `json:"usage,omitempty"`
}

// Clarification is the card a turn ended with: what rongo understood, and
// the candidates it offered. The hits each candidate was built from are NOT
// included here — they are large and never sent to the browser; fetch them
// with CandidateHits for the resumed turn only.
type Clarification struct {
	ID int64 `json:"id"`
	// ThreadID is the thread the clarifying message belongs to. A resumed turn
	// continues THIS thread rather than opening a new one — the reader is
	// still in the same conversation, just answering a question rongo asked.
	ThreadID int64 `json:"thread_id"`
	// Understanding is provenance: it is stored so a resumed turn can say what
	// the first one searched for, and so a stored card can be read back years
	// later. It is not sent to the browser — nothing there ever read it, and
	// serialising the model's internal guesses into the page is output nobody
	// asked for. The column stays.
	Understanding ask.Understanding `json:"-"`
	Candidates    []Candidate       `json:"candidates"`
	// Answered is true once a turn resumed from this card. A card is answered
	// once: the handler refuses a second resume with it, before the choice
	// costs a model call. Not sent to the browser — the page derives the same
	// fact from the answer's from_clarification_id, which it needs anyway to
	// tell two open cards in one thread apart.
	Answered bool `json:"-"`
}

// Candidate is one entry on a clarification card, without its hits.
type Candidate struct {
	Idx       int    `json:"idx"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	ModuleKey string `json:"module_key"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
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

// SetTitle replaces the placeholder, but only while the placeholder is still
// what the row holds. `from` is the title the caller saw; a thread the reader
// has renamed in the meantime no longer matches and keeps its own name. The
// title call runs in the background and lands seconds after the question, so
// without the guard a rename in that window would be silently overwritten.
//
// A failure is swallowed by the caller on purpose: a missing title is
// cosmetic, and it must never fail a turn.
func (s *Store) SetTitle(ctx context.Context, id int64, from, to string) error {
	to = strings.TrimSpace(to)
	if to == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE threads SET title = ? WHERE id = ? AND title = ?`, to, id, from)
	if err != nil {
		return fmt.Errorf("set thread title: %w", err)
	}
	return nil
}

// Rename gives a thread the title its owner typed. Reports whether a row
// matched: a thread that is gone, or was never this reader's, is not an error
// here, it is a 404 at the edge.
func (s *Store) Rename(ctx context.Context, subject string, id int64, title string) (bool, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE threads SET title = ? WHERE id = ? AND user_subject = ?`, title, id, subject)
	if err != nil {
		return false, fmt.Errorf("rename thread: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rename thread: %w", err)
	}
	return n > 0, nil
}

// Delete removes a thread and, through the schema's cascades, every message,
// citation, clarification, source and usage row hanging off it. The owner is
// part of the predicate rather than a check in front of it, so a thread that
// is not yours is indistinguishable from one that does not exist.
func (s *Store) Delete(ctx context.Context, subject string, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM threads WHERE id = ? AND user_subject = ?`, id, subject)
	if err != nil {
		return false, fmt.Errorf("delete thread: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete thread: %w", err)
	}
	return n > 0, nil
}

// AddQuestion appends a question and returns the message it created. The answer
// arrives later, through Finish.
func (s *Store) AddQuestion(ctx context.Context, threadID int64, audience, language, question string) (Message, error) {
	// The ordinal is computed inside the INSERT, not read and then written.
	// Two tabs submitting on one thread would otherwise compute the same number
	// and the second would hit UNIQUE (thread_id, ordinal) — a 500 for a thing
	// people do routinely.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (thread_id, ordinal, audience, language, question)
		VALUES (?, (SELECT COALESCE(MAX(ordinal), -1) + 1 FROM messages WHERE thread_id = ?), ?, ?, ?)`,
		threadID, threadID, audience, language, question)
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
	return Message{ID: id, Ordinal: next, Audience: audience, Language: language, Question: question}, nil
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
			INSERT INTO citations (message_id, marker, repo, branch, path, start_line, end_line, sha)
			VALUES (?,?,?,?,?,?,?,?)`,
			messageID, c.Marker, c.Repo, c.Branch, c.Path, c.StartLine, c.EndLine, c.SHA); err != nil {
			return fmt.Errorf("store citation %d: %w", c.Marker, err)
		}
	}
	return tx.Commit()
}

// SaveUsage records the paid calls one turn made. Called on EVERY way a turn
// ends — answered, asked back, found nothing, failed — because the gates ran
// either way and a thread total that skipped them would be a lie. Nothing to
// save writes nothing.
func (s *Store) SaveUsage(ctx context.Context, messageID int64, calls []usage.Call) error {
	if len(calls) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range calls {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_usage (message_id, step, model, prompt_tokens, completion_tokens)
			VALUES (?,?,?,?,?)`, messageID, c.Step, c.Model, c.Prompt, c.Completion); err != nil {
			return fmt.Errorf("store usage of %s: %w", c.Step, err)
		}
	}
	return tx.Commit()
}

func (s *Store) calls(ctx context.Context, messageID int64) ([]usage.Call, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT step, model, prompt_tokens, completion_tokens FROM message_usage
		 WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		return nil, fmt.Errorf("read usage: %w", err)
	}
	defer rows.Close()
	out := []usage.Call{}
	for rows.Next() {
		var c usage.Call
		if err := rows.Scan(&c.Step, &c.Model, &c.Prompt, &c.Completion); err != nil {
			return nil, fmt.Errorf("scan usage: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
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

// Message returns one turn by id, or false when it does not belong to a
// thread owned by subject. Re-explaining needs the original question text to
// re-run the answerer from stored sources, without the caller having to load
// the whole thread to find one message in it.
func (s *Store) Message(ctx context.Context, subject string, messageID int64) (Message, bool, error) {
	var m Message
	var created string
	var fromClar sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.thread_id, m.ordinal, m.audience, m.language, m.question, m.answer, m.error, m.from_candidate_idx, m.from_clarification_id, m.created_at
		FROM messages m JOIN threads t ON t.id = m.thread_id
		WHERE m.id = ? AND t.user_subject = ?`, messageID, subject).
		Scan(&m.ID, &m.ThreadID, &m.Ordinal, &m.Audience, &m.Language, &m.Question, &m.Answer, &m.Error, &m.FromCandidateIdx, &fromClar, &created)
	if err == sql.ErrNoRows {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, fmt.Errorf("read message: %w", err)
	}
	m.FromClarificationID = fromClar.Int64
	m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	cites, err := s.citations(ctx, m.ID)
	if err != nil {
		return Message{}, false, err
	}
	m.Citations = cites
	return m, true, nil
}

// Messages returns a thread's turns in order, with their citations.
func (s *Store) Messages(ctx context.Context, subject string, threadID int64) ([]Message, error) {
	// The subject is part of the query rather than checked afterwards: a thread
	// belongs to the person who asked, and a mistake here hands someone else's
	// conversation over.
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.ordinal, m.audience, m.language, m.question, m.answer, m.error, m.from_candidate_idx, m.from_clarification_id, m.created_at
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
		var fromClar sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Ordinal, &m.Audience, &m.Language, &m.Question, &m.Answer, &m.Error, &m.FromCandidateIdx, &fromClar, &created); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.FromClarificationID = fromClar.Int64
		m.ThreadID = threadID
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
		clar, err := s.Clarification(ctx, subject, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Clarification = clar
		calls, err := s.calls(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Calls = calls
	}
	return out, nil
}

func (s *Store) citations(ctx context.Context, messageID int64) ([]ask.Citation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT marker, repo, branch, path, start_line, end_line, sha FROM citations
		 WHERE message_id = ? ORDER BY marker`, messageID)
	if err != nil {
		return nil, fmt.Errorf("read citations: %w", err)
	}
	defer rows.Close()
	out := []ask.Citation{}
	for rows.Next() {
		var c ask.Citation
		if err := rows.Scan(&c.Marker, &c.Repo, &c.Branch, &c.Path, &c.StartLine, &c.EndLine, &c.SHA); err != nil {
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

// Clarify writes the clarification and every one of its candidates in one
// transaction: a card that only has some of its candidates would offer a
// choice it cannot honour.
func (s *Store) Clarify(ctx context.Context, messageID int64, c ask.Clarification) (int64, error) {
	understanding, err := json.Marshal(c.Understanding)
	if err != nil {
		return 0, fmt.Errorf("marshal understanding: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO clarifications (message_id, understanding) VALUES (?, ?)`,
		messageID, string(understanding))
	if err != nil {
		return 0, fmt.Errorf("store clarification: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store clarification: %w", err)
	}

	for idx, cand := range c.Candidates {
		hits, err := json.Marshal(cand.Hits)
		if err != nil {
			return 0, fmt.Errorf("marshal candidate %d hits: %w", idx, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO clarification_candidates (clarification_id, idx, repo, branch, module_key, title, summary, hits)
			VALUES (?,?,?,?,?,?,?,?)`,
			id, idx, cand.Repo, cand.Branch, cand.ModuleKey, cand.Title, cand.Summary, string(hits)); err != nil {
			return 0, fmt.Errorf("store candidate %d: %w", idx, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// Clarification returns the card a message ended with, or nil when the
// message did not end in one, or the message does not belong to a thread
// owned by subject. A foreign id yields nil, never someone else's card — the
// caller (Messages) already knows the subject, but every entry point that
// takes a bare id off the wire re-checks it here rather than trusting a
// handler to have done so.
func (s *Store) Clarification(ctx context.Context, subject string, messageID int64) (*Clarification, error) {
	var c Clarification
	var understanding string
	// Whether the card was answered is asked of the messages, not of a flag on
	// the card: a clarification is closed by the answer that came out of it,
	// and that link already exists on the answering turn.
	err := s.db.QueryRowContext(ctx, `
		SELECT c.id, m.thread_id, c.understanding,
		       EXISTS (SELECT 1 FROM messages a WHERE a.from_clarification_id = c.id)
		FROM clarifications c
		JOIN messages m ON m.id = c.message_id
		JOIN threads t ON t.id = m.thread_id
		WHERE c.message_id = ? AND t.user_subject = ?`, messageID, subject).
		Scan(&c.ID, &c.ThreadID, &understanding, &c.Answered)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read clarification: %w", err)
	}
	if err := json.Unmarshal([]byte(understanding), &c.Understanding); err != nil {
		return nil, fmt.Errorf("unmarshal understanding: %w", err)
	}

	// hits is deliberately not selected here: it is large JSON, never sent to
	// the browser, and only the resumed turn reads it, via CandidateHits.
	rows, err := s.db.QueryContext(ctx, `
		SELECT idx, repo, branch, module_key, title, summary
		FROM clarification_candidates WHERE clarification_id = ? ORDER BY idx`, c.ID)
	if err != nil {
		return nil, fmt.Errorf("read candidates: %w", err)
	}
	defer rows.Close()
	c.Candidates = []Candidate{}
	for rows.Next() {
		var cand Candidate
		if err := rows.Scan(&cand.Idx, &cand.Repo, &cand.Branch, &cand.ModuleKey, &cand.Title, &cand.Summary); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		c.Candidates = append(c.Candidates, cand)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &c, nil
}

// CandidateHits returns the understanding and the hits one candidate on a
// card was built from, for the resumed turn: the answer must be built from
// exactly what the card offered, not a fresh search that could rank
// differently. A clarification that does not belong to a thread owned by
// subject yields sql.ErrNoRows, wrapped like any other read failure — never
// another user's hits.
func (s *Store) CandidateHits(ctx context.Context, subject string, clarificationID int64, idx int) (ask.Understanding, []retrieve.Hit, error) {
	var understanding, hits string
	err := s.db.QueryRowContext(ctx, `
		SELECT c.understanding, cc.hits
		FROM clarification_candidates cc
		JOIN clarifications c ON c.id = cc.clarification_id
		JOIN messages m ON m.id = c.message_id
		JOIN threads t ON t.id = m.thread_id
		WHERE cc.clarification_id = ? AND cc.idx = ? AND t.user_subject = ?`, clarificationID, idx, subject).
		Scan(&understanding, &hits)
	if err != nil {
		return ask.Understanding{}, nil, fmt.Errorf("read candidate hits: %w", err)
	}
	var u ask.Understanding
	if err := json.Unmarshal([]byte(understanding), &u); err != nil {
		return ask.Understanding{}, nil, fmt.Errorf("unmarshal understanding: %w", err)
	}
	var h []retrieve.Hit
	if err := json.Unmarshal([]byte(hits), &h); err != nil {
		return ask.Understanding{}, nil, fmt.Errorf("unmarshal hits: %w", err)
	}
	return u, h, nil
}

// LinkChoice records which candidate a new turn resumed from. It is stored on
// the new message, never as a `chosen` flag on the clarification: the answer
// is what closes the card, so a turn that never produced one leaves it open
// for a retry, and nothing in the record is ever overwritten.
//
// The UPDATE only fires when messageID and clarificationID both resolve into
// the SAME thread owned by subject. A handler is never trusted to have paired
// two browser-supplied ids correctly: a mismatched pair would silently
// attribute an answer to a card it never came from, which corrupts the record
// this whole task exists to keep honest. A mismatch — cross-thread,
// cross-user, or either id simply wrong — updates zero rows and errors.
func (s *Store) LinkChoice(ctx context.Context, subject string, messageID, clarificationID int64, idx int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE messages SET from_clarification_id = ?, from_candidate_idx = ?
		WHERE id = ?
		  AND thread_id = (
		      SELECT t.id
		      FROM threads t
		      JOIN messages cm ON cm.thread_id = t.id
		      JOIN clarifications cl ON cl.message_id = cm.id
		      WHERE cl.id = ? AND t.user_subject = ?
		  )`,
		clarificationID, idx, messageID, clarificationID, subject)
	if err != nil {
		return fmt.Errorf("link choice: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("link choice: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("link choice: message %d and clarification %d are not in the same thread owned by this subject", messageID, clarificationID)
	}
	return nil
}

// SaveSources records what an answer was actually written from, as chunk ids.
func (s *Store) SaveSources(ctx context.Context, messageID int64, sources []ask.Source) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, src := range sources {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_sources (message_id, chunk_id, reason, hop) VALUES (?,?,?,?)`,
			messageID, src.ChunkID, src.Reason, src.Hop); err != nil {
			return fmt.Errorf("store source %d: %w", src.ChunkID, err)
		}
	}
	return tx.Commit()
}

// Sources resolves an answer's chunk ids back to their text, ordered by hop
// then chunk id. A chunk a re-index removed no longer joins and is silently
// omitted from the returned slice. A message that does not belong to a
// thread owned by subject yields an empty slice, the same shape as "no
// sources yet", never another user's evidence.
//
// Sources also reports total: how many rows message_sources actually holds
// for this message, scoped by the same ownership check. The caller decides
// what an incomplete set means (Sources itself does not know), but it can
// only decide correctly by comparing len(returned) against total — a
// re-index that removed SOME of the evidence looks identical to one that
// removed none of it if only the resolved slice is visible.
func (s *Store) Sources(ctx context.Context, subject string, messageID int64) (sources []ask.Source, total int, err error) {
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM message_sources ms
		JOIN messages m ON m.id = ms.message_id
		JOIN threads t ON t.id = m.thread_id
		WHERE ms.message_id = ? AND t.user_subject = ?`, messageID, subject).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count sources: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT ms.chunk_id, f.repo, r.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text, ms.reason, ms.hop
		FROM message_sources ms
		JOIN chunks c ON c.id = ms.chunk_id
		JOIN files f ON f.id = c.file_id
		JOIN repo_state r ON r.name = f.repo
		JOIN messages m ON m.id = ms.message_id
		JOIN threads t ON t.id = m.thread_id
		WHERE ms.message_id = ? AND t.user_subject = ?
		ORDER BY ms.hop, ms.chunk_id`, messageID, subject)
	if err != nil {
		return nil, 0, fmt.Errorf("read sources: %w", err)
	}
	defer rows.Close()
	out := []ask.Source{}
	for rows.Next() {
		var src ask.Source
		if err := rows.Scan(&src.ChunkID, &src.Repo, &src.Branch, &src.Path, &src.SHA, &src.Symbol, &src.StartLine, &src.EndLine, &src.Text, &src.Reason, &src.Hop); err != nil {
			return nil, 0, fmt.Errorf("scan source: %w", err)
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
