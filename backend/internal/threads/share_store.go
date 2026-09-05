package threads

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// ErrNoShare is what every public lookup returns for a token that is unknown,
// revoked, or whose thread has been deleted. One error for all three on
// purpose: a link that answered differently for "never existed" and "revoked
// yesterday" would be an oracle for guessing tokens.
var ErrNoShare = errors.New("no such share")

// ErrUnfinished refuses to freeze a thread whose newest turn is still being
// written. The question row exists from the moment it is asked, so a link made
// mid-stream would carry half a sentence for good.
var ErrUnfinished = errors.New("the last turn is not finished")

// Share is one thread readable by anyone holding the link.
type Share struct {
	Token string `json:"token"`
	// The path, never an absolute URL: the browser puts its own origin in
	// front. A rongo behind a TLS-terminating proxy only ever sees plain HTTP
	// and would build the wrong one.
	Path     string `json:"path"`
	ThreadID int64  `json:"thread_id"`
	Title    string `json:"title"`
	// The ceiling. Turns above it are on the record but not on the link.
	UpToMessageID int64     `json:"up_to_message_id"`
	Turns         int       `json:"turns"`
	Newer         int       `json:"newer"`
	SharedAt      time.Time `json:"shared_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SharePath is where a share link lives. The SPA reads it out of
// window.location, and backend/web serves index.html for it like any other
// non-/api path.
const SharePath = "/share/"

// newShareToken mints 128 bits as 22 URL-safe characters. Never a thread id
// and never a slug: the URL is the whole authorisation, so it has to be
// unguessable on its own.
func newShareToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate share token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newestMessage is the id of the last row in a thread, and whether that row is
// a finished turn. A turn is finished once it has an answer, an error, or a
// clarification card — the three ways a turn can end.
func (s *Store) newestMessage(ctx context.Context, subject string, threadID int64) (int64, bool, error) {
	var id int64
	var answer, errText string
	var carded bool
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.answer, m.error,
		       EXISTS (SELECT 1 FROM clarifications c WHERE c.message_id = m.id)
		FROM messages m JOIN threads t ON t.id = m.thread_id
		WHERE m.thread_id = ? AND t.user_subject = ?
		ORDER BY m.id DESC LIMIT 1`, threadID, subject).Scan(&id, &answer, &errText, &carded)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read newest message: %w", err)
	}
	return id, answer != "" || errText != "" || carded, nil
}

// Share makes this thread readable by anyone holding the link, or un-revokes
// the link it already had. Either way the ceiling moves to the newest turn.
//
// A thread that was shared before keeps its token: the link may already be in
// somebody's inbox, and minting a second one would leave the first revoked
// without anybody being told.
func (s *Store) Share(ctx context.Context, subject string, threadID int64) (Share, error) {
	newest, done, err := s.newestMessage(ctx, subject, threadID)
	if err != nil {
		return Share{}, err
	}
	if newest == 0 {
		// No turn, or not this reader's thread. Both are "nothing to share",
		// and telling them apart would say whose thread it is.
		return Share{}, ErrNoShare
	}
	if !done {
		return Share{}, ErrUnfinished
	}

	token, err := newShareToken()
	if err != nil {
		return Share{}, err
	}
	// One statement, so a thread shared from two tabs at once cannot end up
	// with two rows. The conflict keeps the token already handed out and only
	// moves the ceiling.
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO shared_threads (token, thread_id, user_subject, up_to_message_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (thread_id) DO UPDATE SET
			revoked = 0,
			up_to_message_id = excluded.up_to_message_id,
			updated_at = datetime('now')`,
		token, threadID, subject, newest); err != nil {
		return Share{}, fmt.Errorf("share thread: %w", err)
	}
	return s.ShareFor(ctx, subject, threadID)
}

// RaiseShare moves the ceiling to the newest turn, so the turns asked since
// the link was made become part of it. The token does not change: the point of
// Update is that the link already sent out keeps working.
func (s *Store) RaiseShare(ctx context.Context, subject string, threadID int64) (Share, error) {
	newest, done, err := s.newestMessage(ctx, subject, threadID)
	if err != nil {
		return Share{}, err
	}
	if newest == 0 {
		return Share{}, ErrNoShare
	}
	if !done {
		return Share{}, ErrUnfinished
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE shared_threads SET up_to_message_id = ?, updated_at = datetime('now')
		WHERE thread_id = ? AND user_subject = ? AND revoked = 0`,
		newest, threadID, subject)
	if err != nil {
		return Share{}, fmt.Errorf("raise share: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Share{}, ErrNoShare
	}
	return s.ShareFor(ctx, subject, threadID)
}

// RevokeShare turns the link off. The row and the token survive, so sharing
// the thread again hands back the same link rather than a second one to chase.
func (s *Store) RevokeShare(ctx context.Context, subject string, threadID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE shared_threads SET revoked = 1, updated_at = datetime('now')
		WHERE thread_id = ? AND user_subject = ? AND revoked = 0`, threadID, subject)
	if err != nil {
		return false, fmt.Errorf("revoke share: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// shareColumns is the row every share read builds a Share from. Turns and
// Newer are counted in the same statement rather than looked up afterwards:
// both are shown on every row of the Shared page, and a per-row query there
// would be one round trip per link.
const shareColumns = `
	SELECT sh.token, sh.thread_id, t.title, sh.up_to_message_id, sh.shared_at, sh.updated_at,
	       (SELECT COUNT(*) FROM messages m WHERE m.thread_id = sh.thread_id AND m.id <= sh.up_to_message_id),
	       (SELECT COUNT(*) FROM messages m WHERE m.thread_id = sh.thread_id AND m.id > sh.up_to_message_id)
	FROM shared_threads sh JOIN threads t ON t.id = sh.thread_id`

func scanShare(row interface{ Scan(...any) error }) (Share, error) {
	var sh Share
	var sharedAt, updatedAt string
	if err := row.Scan(&sh.Token, &sh.ThreadID, &sh.Title, &sh.UpToMessageID,
		&sharedAt, &updatedAt, &sh.Turns, &sh.Newer); err != nil {
		return Share{}, err
	}
	sh.Path = SharePath + sh.Token
	sh.SharedAt, _ = time.Parse("2006-01-02 15:04:05", sharedAt)
	sh.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)
	return sh, nil
}

// ShareFor is the live link on one of this reader's threads, or ErrNoShare
// when the thread has none. It is what the dialog opens on.
func (s *Store) ShareFor(ctx context.Context, subject string, threadID int64) (Share, error) {
	row := s.db.QueryRowContext(ctx,
		shareColumns+` WHERE sh.thread_id = ? AND sh.user_subject = ? AND sh.revoked = 0`,
		threadID, subject)
	sh, err := scanShare(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNoShare
	}
	if err != nil {
		return Share{}, fmt.Errorf("read share: %w", err)
	}
	return sh, nil
}

// Shares is every live link this reader owns, newest first. A revoked one is
// not listed: it is not a link any more, and keeping it as a greyed row would
// turn the one audit view into a history.
func (s *Store) Shares(ctx context.Context, subject string) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx,
		shareColumns+` WHERE sh.user_subject = ? AND sh.revoked = 0 ORDER BY sh.id DESC`, subject)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, fmt.Errorf("scan share: %w", err)
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// SharedIDs is the set of this reader's threads that are live on a link, for
// the marker on a rail row. One query for the whole list rather than a column
// on the thread list's own statement: the rail is read on every turn, and the
// join would be paid there whether or not anything is shared.
func (s *Store) SharedIDs(ctx context.Context, subject string) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT thread_id FROM shared_threads WHERE user_subject = ? AND revoked = 0`, subject)
	if err != nil {
		return nil, fmt.Errorf("list shared threads: %w", err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan shared thread: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SharedThread is the public read boundary: the one method here that is
// deliberately not scoped to a subject, because the token is the
// authorisation. Everything it returns is capped at the share's ceiling.
//
// It reads through the same messages() every signed-in read goes through, so
// the two can never disagree about what a turn contains.
func (s *Store) SharedThread(ctx context.Context, token string) (Share, []Message, error) {
	row := s.db.QueryRowContext(ctx,
		shareColumns+` WHERE sh.token = ? AND sh.revoked = 0`, token)
	sh, err := scanShare(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, nil, ErrNoShare
	}
	if err != nil {
		return Share{}, nil, fmt.Errorf("read share: %w", err)
	}
	msgs, err := s.messages(ctx, anySubject, sh.ThreadID, sh.UpToMessageID)
	if err != nil {
		return Share{}, nil, err
	}
	return sh, msgs, nil
}

// SharedCitation reports whether this share cites that exact file at that
// exact commit. It is the whole authorisation of the public source viewer:
// /api/source takes any repo/path/sha and would be a reader for the entire
// indexed corpus, so the public route serves only what the turns on the link
// were actually written from.
func (s *Store) SharedCitation(ctx context.Context, token, repo, path, sha string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1
		FROM citations c
		JOIN messages m ON m.id = c.message_id
		JOIN shared_threads sh ON sh.thread_id = m.thread_id
		WHERE sh.token = ? AND sh.revoked = 0 AND m.id <= sh.up_to_message_id
		  AND c.repo = ? AND c.path = ? AND c.sha = ?
		LIMIT 1`, token, repo, path, sha).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read shared citation: %w", err)
	}
	return true, nil
}
