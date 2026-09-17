package sourceview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
)

// Commit is one commit as the commit view shows it: the message, the date,
// and the files it touched with their line counts. No author, on purpose:
// this reaches a shared page.
type Commit struct {
	Repo        string       `json:"repo"`
	Branch      string       `json:"branch"`
	SHA         string       `json:"sha"`
	CommittedAt string       `json:"committed_at"`
	Subject     string       `json:"subject"`
	Body        string       `json:"body"`
	Files       []FileChange `json:"files"`
}

// FileChange is one path of the commit. Indexed says whether the source
// viewer can open it: a path the indexer skipped, or one the commit
// deleted, has no file to show.
type FileChange struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Indexed bool   `json:"indexed"`
}

// CommitReader is the checkout as the commit view needs it. *gitrepo.Client
// satisfies it.
type CommitReader interface {
	Show(ctx context.Context, spec repos.Spec, sha string) (gitrepo.CommitDetail, error)
}

// WithCommits gives the service the checkout's commit reader. Without it
// the commit view answers not found, which is what a test service without
// a git binary wants.
func (s *Service) WithCommits(r CommitReader) *Service {
	s.commits = r
	return s
}

// Commit returns the commit sha of repo. The commits row is the permission,
// the way the files row is for Read: only a commit the lane recorded is
// served, so the view is the evidence behind a changes answer and not a
// browser over every commit of every checkout.
func (s *Service) Commit(ctx context.Context, repo, sha string) (Commit, error) {
	if s.commits == nil {
		return Commit{}, fmt.Errorf("%w: no commit reader", ErrNotFound)
	}
	if !shaRe.MatchString(sha) {
		return Commit{}, fmt.Errorf("%w: commit %q", ErrInvalid, sha)
	}
	var branch string
	err := s.db.QueryRowContext(ctx, `SELECT branch FROM repo_state WHERE name = ?`, repo).Scan(&branch)
	if errors.Is(err, sql.ErrNoRows) {
		return Commit{}, fmt.Errorf("%w: unknown repository %q", ErrNotFound, repo)
	}
	if err != nil {
		return Commit{}, fmt.Errorf("look up repository %q: %w", repo, err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM commits WHERE repo = ? AND sha = ?`, repo, sha).Scan(&n); err != nil {
		return Commit{}, fmt.Errorf("look up commit %s/%s: %w", repo, sha, err)
	}
	if n == 0 {
		return Commit{}, fmt.Errorf("%w: %s/%s is not in the commit lane", ErrNotFound, repo, sha)
	}
	d, err := s.commits.Show(ctx, repos.Spec{Name: repo}, sha)
	if err != nil {
		return Commit{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	out := Commit{Repo: repo, Branch: branch, SHA: d.SHA, CommittedAt: d.CommittedAt, Subject: d.Subject, Body: d.Body, Files: []FileChange{}}
	for _, f := range d.Files {
		var indexed int
		if err := s.db.QueryRowContext(ctx,
			`SELECT count(*) FROM files WHERE repo = ? AND path = ? AND skip_reason = ''`, repo, f.Path).Scan(&indexed); err != nil {
			return Commit{}, fmt.Errorf("look up %s/%s: %w", repo, f.Path, err)
		}
		out.Files = append(out.Files, FileChange{Path: f.Path, Added: f.Added, Deleted: f.Deleted, Indexed: indexed > 0})
	}
	return out, nil
}
