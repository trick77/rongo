// Package history is the commit lane: the first-parent history of every
// indexed branch, stored beside the file index and searched by date window
// and topic. It is what answers "what changed since Monday" from the record
// rather than from whichever document narrates a change.
package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/retrieve"
)

// Store reads and writes the commits table and its FTS mirror.
type Store struct {
	db *sql.DB
}

// New builds a Store over the shared database.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Sync makes one repository's rows the given first-parent list, which is
// what every index run hands it: the branch's history from head, bounded
// by depth. A commit already held keeps its row and its id, so a full
// re-index does not orphan the changes turns that cite it; a commit no
// longer in the list goes, so a rebase, an amend, a force-push or a branch
// change leaves neither a stale change nor two versions of one answerable.
// A poll that failed after writing is safe to redo for the same reason.
func (s *Store) Sync(ctx context.Context, repo string, commits []gitrepo.Commit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insert(ctx, tx, repo, commits); err != nil {
		return err
	}
	if err := dropAbsent(ctx, tx, repo, commits); err != nil {
		return err
	}
	return tx.Commit()
}

// dropAbsent removes the repository's rows whose sha the list no longer
// carries, mirror first, like PurgeTx.
func dropAbsent(ctx context.Context, tx *sql.Tx, repo string, commits []gitrepo.Commit) error {
	keep := make(map[string]bool, len(commits))
	for _, c := range commits {
		keep[c.SHA] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, sha FROM commits WHERE repo = ?`, repo)
	if err != nil {
		return err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		var sha string
		if err := rows.Scan(&id, &sha); err != nil {
			rows.Close()
			return err
		}
		if !keep[sha] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM commits_fts WHERE rowid = ?`, id); err != nil {
			return fmt.Errorf("drop commit %d from commits_fts: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM commits WHERE id = ?`, id); err != nil {
			return fmt.Errorf("drop commit %d: %w", id, err)
		}
	}
	return nil
}

func insert(ctx context.Context, tx *sql.Tx, repo string, commits []gitrepo.Commit) error {
	for _, c := range commits {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM commits WHERE repo = ? AND sha = ?`,
			repo, c.SHA).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		at, err := time.Parse(time.RFC3339, c.CommittedAt)
		if err != nil {
			return fmt.Errorf("commit %s: date %q: %w", gitrepo.ShortSHA(c.SHA), c.CommittedAt, err)
		}
		paths := strings.Join(c.Paths, "\n")
		res, err := tx.ExecContext(ctx, `
			INSERT INTO commits (repo, sha, committed_at, author, subject, body, paths)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			repo, c.SHA, at.UTC().Format(time.RFC3339), c.Author, c.Subject, c.Body, paths)
		if err != nil {
			return fmt.Errorf("insert commit %s: %w", gitrepo.ShortSHA(c.SHA), err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO commits_fts (rowid, subject, body, paths) VALUES (?, ?, ?, ?)`,
			id, c.Subject, c.Body, strings.ReplaceAll(paths, "/", " ")); err != nil {
			return fmt.Errorf("mirror commit %s: %w", gitrepo.ShortSHA(c.SHA), err)
		}
	}
	return nil
}

// PurgeTx removes one repository's commits inside the caller's transaction,
// mirror first: an fts5 table takes part in no cascade, and an orphaned
// mirror row resolves against whatever commit is written next.
func PurgeTx(ctx context.Context, tx *sql.Tx, repo string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM commits_fts WHERE rowid IN (
		SELECT id FROM commits WHERE repo = ?)`, repo); err != nil {
		return fmt.Errorf("purge %s from commits_fts: %w", repo, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM commits WHERE repo = ?`, repo); err != nil {
		return fmt.Errorf("purge %s from commits: %w", repo, err)
	}
	return nil
}

// Query is one "what changed" question, resolved: which repositories, how
// far back, and about what.
type Query struct {
	// Repos restricts the search; empty means every enabled repository. A
	// parked one is never searched either way, the rule every retrieval
	// lane follows.
	Repos []string
	// Since is the start of the window, inclusive.
	Since time.Time
	// Topic narrows to commits whose subject, body or paths mention it;
	// empty lists the window whole.
	Topic string
	// Limit caps the result; newest first when there is no topic, best
	// match first when there is.
	Limit int
}

// Commit is one search result. Author is deliberately not here.
type Commit struct {
	// ID is the commits row, what a turn's record stores.
	ID          int64
	Repo        string
	Branch      string
	SHA         string
	CommittedAt time.Time
	Subject     string
	Body        string
	Paths       []string
}

// Search lists the commits of the window. With a topic the keyword lane's
// widest rung (content words as prefixes, ORed) filters the window and
// bm25 orders it; without one the window is listed newest first. Either
// way the window is applied in SQL, never after a limit, so a busy month
// cannot push the asked days out of the result.
func (s *Store) Search(ctx context.Context, q Query) ([]Commit, error) {
	if q.Limit <= 0 {
		q.Limit = 40
	}
	args := []any{}
	var b strings.Builder
	b.WriteString(`SELECT c.id, c.repo, r.branch, c.sha, c.committed_at, c.subject, c.body, c.paths
		FROM commits c JOIN repo_state r ON r.name = c.repo`)
	match := topicMatch(q.Topic)
	if match != "" {
		b.WriteString(` JOIN commits_fts x ON x.rowid = c.id`)
	}
	b.WriteString(` WHERE r.enabled = 1 AND c.committed_at >= ?`)
	args = append(args, q.Since.UTC().Format(time.RFC3339))
	if len(q.Repos) > 0 {
		b.WriteString(` AND c.repo IN (` + placeholders(len(q.Repos)) + `)`)
		for _, r := range q.Repos {
			args = append(args, r)
		}
	}
	if match != "" {
		b.WriteString(` AND commits_fts MATCH ?`)
		args = append(args, match)
		b.WriteString(` ORDER BY bm25(commits_fts), c.committed_at DESC, c.repo`)
	} else {
		b.WriteString(` ORDER BY c.committed_at DESC, c.repo, c.sha`)
	}
	b.WriteString(` LIMIT ?`)
	args = append(args, q.Limit)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("commit search: %w", err)
	}
	defer rows.Close()
	out := []Commit{}
	for rows.Next() {
		var c Commit
		var at, paths string
		if err := rows.Scan(&c.ID, &c.Repo, &c.Branch, &c.SHA, &at, &c.Subject, &c.Body, &paths); err != nil {
			return nil, err
		}
		c.CommittedAt, _ = time.Parse(time.RFC3339, at)
		if paths != "" {
			c.Paths = strings.Split(paths, "\n")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// topicMatch is the widest rung of retrieve's keyword ladder over the
// topic: content words, prefixed, ORed. A commit message is short, so any
// one of the reader's words in it is a hit worth ranking.
func topicMatch(topic string) string {
	tiers := retrieve.BuildFTSQueries(topic)
	if len(tiers) == 0 {
		return ""
	}
	return tiers[len(tiers)-1].Match
}

// Count reports how many commits a repository holds and the newest date,
// for the Repos page.
func (s *Store) Count(ctx context.Context, repo string) (n int, newest time.Time, err error) {
	var at sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*), max(committed_at) FROM commits WHERE repo = ?`, repo).Scan(&n, &at); err != nil {
		return 0, time.Time{}, err
	}
	if at.Valid {
		newest, _ = time.Parse(time.RFC3339, at.String)
	}
	return n, newest, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
