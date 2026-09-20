package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The rail asks for 30, the Threads page for 50 at a time; nothing needs
// more than a thousand in one answer. ../loom's figures.
const (
	DefaultListLimit = 30
	MaxListLimit     = 1000
)

// ListOptions is what a paged read of the thread list takes.
type ListOptions struct {
	// Limit is how many rows to answer with; zero means DefaultListLimit, and
	// anything past MaxListLimit is cut to it.
	Limit int
	// Cursor is the public id of the last thread of the previous page: the
	// page after it starts below that row. Empty for the first page. A public
	// id and not a row number, because it stands in a URL.
	Cursor string
	// StarredOnly narrows the page to threads the reader starred. The rail's
	// starred section reads with it, so a starred thread stays listed however
	// many newer threads there are.
	StarredOnly bool
}

// ThreadPage is one page of the list and, when there is one, the cursor the
// next page is asked with.
type ThreadPage struct {
	Items []Thread `json:"items"`
	// NextCursor is empty on the last page. Nullable in JSON, because "no
	// more" and "" are the same thing and null says it plainly.
	NextCursor *string `json:"next_cursor"`
}

// EffectiveLimit is the row count a request for n actually gets.
func EffectiveLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultListLimit
	case n > MaxListLimit:
		return MaxListLimit
	}
	return n
}

// ListPage returns one page of a user's threads, newest first, and the
// cursor for the page after it. A cursor naming a thread that is not this
// reader's — or none, because the row was deleted between two pages — ends
// the list rather than starting it over: the browser APPENDS the page it is
// handed, and page one under page N would be every row twice.
func (s *Store) ListPage(ctx context.Context, subject string, opts ListOptions) (ThreadPage, error) {
	limit := EffectiveLimit(opts.Limit)
	// The cursor is resolved to the row number here, owner-checked, and never
	// leaves: `id < ?` is the whole of the paging, since a thread's row id
	// only ever grows with its age.
	var after int64
	if opts.Cursor != "" {
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM threads WHERE public_id = ? AND user_subject = ?`, opts.Cursor, subject).Scan(&after)
		if errors.Is(err, sql.ErrNoRows) {
			return ThreadPage{Items: []Thread{}}, nil
		}
		if err != nil {
			return ThreadPage{}, fmt.Errorf("resolve cursor: %w", err)
		}
	}
	var (
		rows *sql.Rows
		err  error
	)
	starred := ""
	if opts.StarredOnly {
		starred = " AND starred = 1"
	}
	if after > 0 {
		rows, err = s.db.QueryContext(ctx, `
			SELECT `+threadColumns+` FROM threads
			WHERE user_subject = ? AND id < ?`+starred+` ORDER BY id DESC LIMIT ?`, subject, after, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT `+threadColumns+` FROM threads
			WHERE user_subject = ?`+starred+` ORDER BY id DESC LIMIT ?`, subject, limit)
	}
	if err != nil {
		return ThreadPage{}, fmt.Errorf("list threads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items, err := scanThreads(rows)
	if err != nil {
		return ThreadPage{}, err
	}
	if err := s.markShared(ctx, subject, items); err != nil {
		return ThreadPage{}, err
	}
	page := ThreadPage{Items: items}
	// A full page may have more behind it; a short one cannot. The last page
	// of a list that divides evenly costs one empty request, which is cheaper
	// than a count on every page.
	if len(items) == limit {
		cursor := items[len(items)-1].PublicID
		page.NextCursor = &cursor
	}
	return page, nil
}

// Get is one thread's row, for the header of a thread the rail's page does
// not carry. Not the owner's → not found, like every other read by address.
func (s *Store) Get(ctx context.Context, subject string, threadID int64) (Thread, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+threadColumns+` FROM threads
		WHERE id = ? AND user_subject = ?`, threadID, subject)
	if err != nil {
		return Thread{}, false, fmt.Errorf("get thread: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items, err := scanThreads(rows)
	if err != nil || len(items) == 0 {
		return Thread{}, false, err
	}
	if err := s.markShared(ctx, subject, items); err != nil {
		return Thread{}, false, err
	}
	return items[0], true, nil
}

// Hit is a thread the search found and, when it was found by what was said
// in it, the passage that matched.
type Hit struct {
	Thread
	// Snippet is a piece of the matching question or answer with the matched
	// words between « and », and … where it was cut. Empty for a thread found
	// by its title alone.
	Snippet string `json:"snippet,omitempty"`
}

// MaxSearchResults is the most one search answers with. Bounded only to keep
// one page's render cheap; the search itself is not paged.
const MaxSearchResults = 200

// Search finds a reader's threads by title and by what was said in them.
// Title matches come first, in list order, then threads matched only through
// a message, best match first; a thread found both ways stays in its title
// position and carries the snippet. ../loom's merge, done here rather than in
// the browser because one request is enough.
func (s *Store) Search(ctx context.Context, subject, query string, limit int) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []Hit{}, nil
	}
	if limit <= 0 || limit > MaxSearchResults {
		limit = MaxSearchResults
	}
	// Titles through the same index and tokenizer as the messages, so one
	// rule finds both: LIKE folds case for ASCII only and never folds an
	// accent, and "Übersicht" would have answered to ubersicht in an answer but
	// not in a title. List order rather than rank: a title either says the
	// word or does not.
	match := ftsPrefixQuery(query)
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.public_id, t.title, t.title_settled, t.created_at, t.starred
		FROM thread_fts JOIN threads t ON t.rowid = thread_fts.rowid
		WHERE thread_fts MATCH ? AND t.user_subject = ? ORDER BY t.id DESC LIMIT ?`,
		match, subject, limit)
	if err != nil {
		return nil, fmt.Errorf("search titles: %w", err)
	}
	byTitle, err := scanThreads(rows)
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(byTitle))
	at := make(map[int64]int, len(byTitle))
	for _, t := range byTitle {
		at[t.ID] = len(hits)
		hits = append(hits, Hit{Thread: t})
	}

	// Ranked by bm25 across every message, then reduced to one row per
	// thread: the query over-fetches, since a long thread can hold many
	// matching turns and each would otherwise spend one of the limit's slots.
	// snippet() over column -1 picks whichever of question and answer the
	// match is in.
	content, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.public_id, t.title, t.title_settled, t.created_at, t.starred,
		       snippet(message_fts, -1, '«', '»', '…', 32)
		FROM message_fts
		JOIN threads t ON t.id = message_fts.thread_id
		WHERE message_fts MATCH ? AND t.user_subject = ?
		ORDER BY bm25(message_fts) LIMIT ?`,
		match, subject, limit*4)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer func() { _ = content.Close() }()
	seen := map[int64]bool{}
	for content.Next() {
		var (
			t       Thread
			created string
			settled bool
			snippet string
		)
		if err := content.Scan(&t.ID, &t.PublicID, &t.Title, &settled, &created, &t.Starred, &snippet); err != nil {
			return nil, fmt.Errorf("scan hit: %w", err)
		}
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		if i, ok := at[t.ID]; ok {
			hits[i].Snippet = snippet
			continue
		}
		if len(hits) >= limit {
			continue
		}
		t.TitlePending = !settled
		t.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		hits = append(hits, Hit{Thread: t, Snippet: snippet})
	}
	if err := content.Err(); err != nil {
		return nil, err
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	shared, err := s.SharedIDs(ctx, subject)
	if err != nil {
		return nil, err
	}
	for i := range hits {
		hits[i].Shared = shared[hits[i].ID]
	}
	return hits, nil
}

// threadColumns is the list's six columns, in the order scanThreads reads
// them. Every SELECT that feeds scanThreads names them through this, so a
// column added here is added everywhere at once — the scan is positional and
// a missing column fails at run time, not at compile time.
const threadColumns = "id, public_id, title, title_settled, created_at, starred"

// scanThreads reads threadColumns off every row.
func scanThreads(rows *sql.Rows) ([]Thread, error) {
	out := []Thread{}
	for rows.Next() {
		var t Thread
		var created string
		var settled bool
		if err := rows.Scan(&t.ID, &t.PublicID, &t.Title, &settled, &created, &t.Starred); err != nil {
			return nil, fmt.Errorf("scan thread: %w", err)
		}
		t.TitlePending = !settled
		t.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// markShared sets Shared on every row a live link points at. One extra query
// rather than a join: nearly every reader has no share at all.
func (s *Store) markShared(ctx context.Context, subject string, items []Thread) error {
	shared, err := s.SharedIDs(ctx, subject)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Shared = shared[items[i].ID]
	}
	return nil
}

// ftsPrefixQuery turns what was typed into an FTS5 MATCH expression: every
// whitespace-separated term quoted and prefix-matched, all of them required.
// Quoting takes the bare-word syntax out of play, so a "-" or a ":" in what
// someone typed is a character rather than an operator.
func ftsPrefixQuery(query string) string {
	terms := strings.Fields(query)
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, `"`+strings.ReplaceAll(term, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " ")
}
