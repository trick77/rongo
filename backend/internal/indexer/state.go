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
	// Project, Kind, Description and Uses come from repos.yaml, not from the
	// checkout: they say which product this repository belongs to and what part
	// it plays in it. Nothing here is derived from code, and none of it is ever
	// embedded, indexed or cited.
	Project     string
	Kind        string
	Description string
	Uses        []string
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
// in the list is inserted or updated; an entry ABSENT from the list is PURGED —
// its row, its files, its symbols and its chunks in all three tables. It returns
// the names it purged, so the caller can remove their checkouts too.
//
// Removing an entry from the YAML is therefore how rongo is made to forget a
// repository. That reverses the earlier behaviour, which only set enabled = 0:
// nothing filters retrieval on that column, so a "deactivated" repository went
// on answering questions out of an index the Repos page said was retired, and
// the explicit purge the comments promised was never implemented. The cost of
// the reversal is that a mistyped name: re-indexes instead of resuming — cheap,
// because embed_cache is keyed on content hash and not on the repository.
func (s *StateStore) SyncSpecs(ctx context.Context, specs []repos.Spec) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	listed := make(map[string]bool, len(specs))
	for _, spec := range specs {
		listed[spec.Name] = true
	}
	known, err := namesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	var purged []string
	for _, name := range known {
		if listed[name] {
			continue
		}
		if err := purgeRepoTx(ctx, tx, name); err != nil {
			return nil, err
		}
		purged = append(purged, name)
	}

	for _, spec := range specs {
		enabled := 0
		if spec.Enabled {
			enabled = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO repo_state (name, clone_url, branch, enabled, token_env, project, kind, description)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				clone_url = excluded.clone_url,
				-- An omitted branch: means "the remote's default", which is
				-- resolved once and recorded here. Copying the empty value over
				-- it would wipe that on every boot and every reload of the
				-- list, and the branch travels with every citation — a forge
				-- URL without it may 404 off the default branch. A branch named
				-- in the YAML still wins, or a corrected entry would never take
				-- effect.
				--
				-- Unless the clone_url changed: then the recorded branch was
				-- resolved from a DIFFERENT remote and means nothing here. It is
				-- dropped so the next poll resolves it afresh — the corpus mixes
				-- master and main, and keeping "master" across a switch to a
				-- repository whose default is main leaves the entry reporting
				-- "configured branch not found" on every cycle, forever, with
				-- nothing that would ever re-resolve it. This is the only place
				-- that can tell the two apart: a YAML that names a branch is
				-- handled by the clause above, so what is being dropped here is
				-- always a resolved value.
				branch    = CASE
					WHEN excluded.branch <> '' THEN excluded.branch
					WHEN repo_state.clone_url <> excluded.clone_url THEN ''
					ELSE repo_state.branch END,
				enabled   = excluded.enabled,
				token_env = excluded.token_env,
				-- Structure is copied over unconditionally, and deliberately
				-- NOT next to the branch rule above: it is written by hand in
				-- repos.yaml and never resolved from a remote, so there is no
				-- recorded value an empty YAML field could wipe. Editing any of
				-- it leaves last_sha and the checkout alone — a structure edit
				-- says what a repository is for, not what is in it.
				project     = excluded.project,
				kind        = excluded.kind,
				description = excluded.description`,
			spec.Name, spec.CloneURL, spec.Branch, enabled, spec.TokenEnv,
			spec.Project, spec.Kind, spec.Description,
		); err != nil {
			return nil, fmt.Errorf("upsert %s: %w", spec.Name, err)
		}

		// Replace rather than insert, for repodeps.Sync's reason: a repository
		// that drops an edge must stop declaring it, or the Projects page goes
		// on drawing an arrow that no longer exists.
		if _, err := tx.ExecContext(ctx, `DELETE FROM repo_uses WHERE repo = ?`, spec.Name); err != nil {
			return nil, fmt.Errorf("clear repo_uses for %s: %w", spec.Name, err)
		}
		for _, u := range spec.Uses {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO repo_uses (repo, uses) VALUES (?, ?)`, spec.Name, u); err != nil {
				return nil, fmt.Errorf("insert repo_uses for %s: %w", spec.Name, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return purged, nil
}

// ResetRepo drops a repository's indexed content but KEEPS its row, so the next
// poll indexes it from scratch. It is what a checkout pointing at the wrong
// remote needs: the entry is still in the YAML and has to survive, only what was
// built out of the wrong code has to go.
//
// last_sha goes with it, and that is the point: leaving it would send the next
// poll into an incremental diff against a commit belonging to another
// repository. last_error goes too — a reset is a fresh start, and the previous
// repository's failure describes code that is no longer here.
func (s *StateStore) ResetRepo(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := purgeContent(ctx, tx, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE repo_state
		SET last_sha = '', last_error = '', file_count = 0, chunk_count = 0
		WHERE name = ?`, name); err != nil {
		return fmt.Errorf("reset %s: %w", name, err)
	}
	return tx.Commit()
}

// namesTx lists every repository the database knows about, inside a transaction.
func namesTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM repo_state ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// purgeRepoTx is purgeContent plus the repo_state row itself, which takes
// repo_deps with it through the cascade.
func purgeRepoTx(ctx context.Context, tx *sql.Tx, name string) error {
	if err := purgeContent(ctx, tx, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM repo_state WHERE name = ?`, name); err != nil {
		return fmt.Errorf("purge %s: %w", name, err)
	}
	return nil
}

// purgeContent removes every file the index holds for one repository.
//
// The rows go one file at a time, each preceded by clearFileContent, and NOT as
// a single "DELETE FROM files WHERE repo = ?". The FK cascade reaches chunks,
// but chunks_vec (vec0) and chunks_fts (fts5) can take part in neither a
// cascade nor a trigger, so a bulk delete would leave both mirrors holding rows
// whose chunks are gone. An orphaned vector is not inert: the semantic lane
// keeps returning it, and rowid == chunks.id then resolves it against whatever
// chunk is written next.
func purgeContent(ctx context.Context, tx *sql.Tx, name string) error {
	// The mirrors go FIRST and by hand. Deleting the files rows cascades to
	// chunks and symbols, but a cascade cannot reach chunks_vec (vec0) or
	// chunks_fts (fts5) — neither can take part in one, or in a trigger. Letting
	// the cascade fire first would strand both, and an orphaned vector is not
	// inert: the semantic lane keeps returning it, and rowid == chunks.id then
	// resolves it against whatever chunk is written next.
	for _, mirror := range []string{"chunks_vec", "chunks_fts"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+mirror+` WHERE rowid IN (
			SELECT c.id FROM chunks c JOIN files f ON f.id = c.file_id WHERE f.repo = ?)`,
			name); err != nil {
			return fmt.Errorf("purge %s from %s: %w", name, mirror, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE repo = ?`, name); err != nil {
		return fmt.Errorf("purge %s: %w", name, err)
	}
	return nil
}

// Active lists the repositories currently in the list and enabled.
func (s *StateStore) Active(ctx context.Context) ([]RepoState, error) {
	return s.states(ctx, `WHERE enabled = 1`)
}

// All lists every repository, active or not, for the Repos status page. A
// deactivated repository still has an index and still deserves to be visible.
func (s *StateStore) All(ctx context.Context) ([]RepoState, error) {
	return s.states(ctx, "")
}

// states is Active and All less their one differing word. They read the same
// nine columns plus the structure and attach the same edges, and keeping two
// copies of that is how one of them ends up a column behind the other.
func (s *StateStore) states(ctx context.Context, where string) ([]RepoState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, clone_url, branch, enabled, last_sha, last_error, last_run_at,
		       file_count, chunk_count, token_env, project, kind, description
		FROM repo_state `+where+` ORDER BY name`)
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
			&r.LastError, &lastRun, &r.Files, &r.Chunks, &r.TokenEnv,
			&r.Project, &r.Kind, &r.Description); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		if lastRun != "" {
			r.LastRunAt, _ = time.Parse(time.RFC3339, lastRun)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.attachUses(ctx, out)
}

// attachUses reads every edge in one query and hands each repository its own.
// One query rather than one per repository: repo_uses holds a handful of rows
// for a handful of repositories, and Active is called on every poll.
//
// Edges are read for the WHOLE table even when the caller asked only for the
// enabled repositories, then dropped for anything not in the result. A disabled
// sibling is still a declared edge, but it is not on the page or in the prompt,
// so pointing at it would draw an arrow to nothing.
func (s *StateStore) attachUses(ctx context.Context, states []RepoState) error {
	if len(states) == 0 {
		return nil
	}
	at := make(map[string]int, len(states))
	for i, r := range states {
		at[r.Name] = i
	}
	rows, err := s.db.QueryContext(ctx, `SELECT repo, uses FROM repo_uses ORDER BY repo, uses`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var repo, uses string
		if err := rows.Scan(&repo, &uses); err != nil {
			return err
		}
		i, ok := at[repo]
		if !ok {
			continue
		}
		if _, ok := at[uses]; !ok {
			continue
		}
		states[i].Uses = append(states[i].Uses, uses)
	}
	return rows.Err()
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

// SetCounts refreshes the index totals alone. The startup sweep uses it after
// removing excluded content: unlike MarkIndexed it moves neither last_sha nor
// last_run_at and leaves a recorded error standing, because no poll ran.
func (s *StateStore) SetCounts(ctx context.Context, name string, c Counts) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE repo_state SET file_count = ?, chunk_count = ? WHERE name = ?`,
		c.Files, c.Chunks, name)
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
