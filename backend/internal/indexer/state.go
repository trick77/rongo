// Package indexer owns the indexing pipeline: keeping checkouts current,
// selecting files, extracting symbols, chunking, embedding and storing.
package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/trick77/rongo/internal/repos"
)

// Counts is what one indexing run produced.
type Counts struct {
	Files  int
	Chunks int
}

// RepoState is a repository's recorded indexing state.
type RepoState struct {
	Name     string
	CloneURL string
	Branch   string
	// TokenEnv names the environment variable holding this repository's forge
	// token. The name travels; the value is read at fetch time and never
	// persisted.
	TokenEnv  string
	Enabled   bool
	LastSHA   string
	LastError string
	LastRunAt time.Time
	Files     int
	Chunks    int
}

// StateStore reads and writes repo_state.
type StateStore struct {
	db *sql.DB
}

// NewStateStore builds a StateStore.
func NewStateStore(db *sql.DB) *StateStore {
	return &StateStore{db: db}
}

// SyncSpecs reconciles the database with the repository list. An entry present
// in the list is inserted or updated; an entry ABSENT from the list is
// deactivated, never deleted — its index survives until an explicit purge,
// because a typo in the YAML must not destroy hours of indexing.
//
// last_sha is deliberately left untouched, so a repository removed and then
// re-added resumes with an incremental diff instead of re-indexing from
// scratch.
func (s *StateStore) SyncSpecs(ctx context.Context, specs []repos.Spec) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE repo_state SET enabled = 0`); err != nil {
		return fmt.Errorf("deactivate all: %w", err)
	}
	for _, spec := range specs {
		enabled := 0
		if spec.Enabled {
			enabled = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO repo_state (name, clone_url, branch, enabled, token_env)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				clone_url = excluded.clone_url,
				branch    = excluded.branch,
				enabled   = excluded.enabled,
				token_env = excluded.token_env`,
			spec.Name, spec.CloneURL, spec.Branch, enabled, spec.TokenEnv,
		); err != nil {
			return fmt.Errorf("upsert %s: %w", spec.Name, err)
		}
	}
	return tx.Commit()
}

// Active lists the repositories currently in the list and enabled.
func (s *StateStore) Active(ctx context.Context) ([]RepoState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, clone_url, branch, enabled, last_sha, last_error, last_run_at,
		       file_count, chunk_count, token_env
		FROM repo_state WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RepoState
	for rows.Next() {
		var r RepoState
		var enabled int
		var lastRun string
		if err := rows.Scan(&r.Name, &r.CloneURL, &r.Branch, &enabled, &r.LastSHA,
			&r.LastError, &lastRun, &r.Files, &r.Chunks, &r.TokenEnv); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		if lastRun != "" {
			r.LastRunAt, _ = time.Parse(time.RFC3339, lastRun)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// All lists every repository, active or not, for the Repos status page. A
// deactivated repository still has an index and still deserves to be visible.
func (s *StateStore) All(ctx context.Context) ([]RepoState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, clone_url, branch, enabled, last_sha, last_error, last_run_at,
		       file_count, chunk_count, token_env
		FROM repo_state ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RepoState
	for rows.Next() {
		var r RepoState
		var enabled int
		var lastRun string
		if err := rows.Scan(&r.Name, &r.CloneURL, &r.Branch, &enabled, &r.LastSHA,
			&r.LastError, &lastRun, &r.Files, &r.Chunks, &r.TokenEnv); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		if lastRun != "" {
			r.LastRunAt, _ = time.Parse(time.RFC3339, lastRun)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkIndexed records a successful run and clears any previous error, so a
// stale failure cannot alarm forever.
func (s *StateStore) MarkIndexed(ctx context.Context, name, sha string, c Counts) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE repo_state
		SET last_sha = ?, last_run_at = ?, last_error = '', file_count = ?, chunk_count = ?
		WHERE name = ?`,
		sha, time.Now().UTC().Format(time.RFC3339), c.Files, c.Chunks, name)
	return err
}

// MarkChecked records a successful poll that found nothing new: it refreshes
// last_run_at and clears last_error, leaving the index counts alone.
//
// Without it a transient fetch failure stays on the Repos page until the
// repository happens to receive a new commit, which is not "a stale failure
// cannot alarm forever" — it is exactly that.
func (s *StateStore) MarkChecked(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE repo_state SET last_error = '', last_run_at = ? WHERE name = ?`,
		time.Now().UTC().Format(time.RFC3339), name)
	return err
}

// MarkError records a failure so the Repos page can show it. A silent stop
// would freeze the index while everything looks healthy and answers quietly
// came from months-old code.
func (s *StateStore) MarkError(ctx context.Context, name, msg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE repo_state SET last_error = ?, last_run_at = ? WHERE name = ?`,
		msg, time.Now().UTC().Format(time.RFC3339), name)
	return err
}

// SetBranch records the branch actually in use, after the git layer resolved an
// omitted one from the remote. Never assume master: the corpus mixes master and
// main.
func (s *StateStore) SetBranch(ctx context.Context, name, branch string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repo_state SET branch = ? WHERE name = ?`,
		branch, name)
	return err
}
