// Package release is the checkout side of a release turn: the ask.Releaser
// the server wires, reading tags and ancestry from the checkouts and the
// indexed head from repo_state. Nothing here is a model or a search.
package release

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/repos"
)

// Git answers from the checkouts under the git client's root.
type Git struct {
	git   *gitrepo.Client
	db    *sql.DB
	depth int
}

// New builds the Releaser. depth is the poller's history depth.
func New(git *gitrepo.Client, db *sql.DB, depth int) *Git {
	return &Git{git: git, db: db, depth: depth}
}

func spec(repo string) repos.Spec { return repos.Spec{Name: repo} }

// ResolveTag is gitrepo's, with its miss mapped onto ask's sentinel.
func (g *Git) ResolveTag(ctx context.Context, repo, tag string) (string, error) {
	sha, err := g.git.ResolveTag(ctx, spec(repo), tag)
	if errors.Is(err, gitrepo.ErrTagUnknown) {
		return "", fmt.Errorf("%s: %q: %w", repo, tag, ask.ErrVersionUnknown)
	}
	return sha, err
}

// IsAncestor is gitrepo's.
func (g *Git) IsAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	return g.git.IsAncestor(ctx, spec(repo), ancestor, descendant)
}

// Range lists the first-parent shas of from..to, newest first.
func (g *Git) Range(ctx context.Context, repo, from, to string, limit int) ([]string, error) {
	commits, err := g.git.Log(ctx, spec(repo), from, to, limit)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		out = append(out, c.SHA)
	}
	return out, nil
}

// Head reads the indexed commit and branch off repo_state, and the remote
// branch's commit off the checkout's remote-tracking ref (no fetch: the
// poller's last fetch is what "not indexed yet" is measured against). A
// snapshot has no remote and its indexed commit is its only commit.
func (g *Git) Head(ctx context.Context, repo string) (ask.RepoHead, error) {
	var h ask.RepoHead
	var cloneURL string
	err := g.db.QueryRowContext(ctx,
		`SELECT last_sha, branch, clone_url FROM repo_state WHERE name = ? AND enabled = 1`, repo).
		Scan(&h.SHA, &h.Branch, &cloneURL)
	if err != nil {
		return ask.RepoHead{}, fmt.Errorf("head of %s: %w", repo, err)
	}
	h.Snapshot = cloneURL == ""
	h.Remote = h.SHA
	if h.Snapshot || h.Branch == "" {
		return h, nil
	}
	remote, err := g.git.HeadSHA(ctx, spec(repo), h.Branch)
	if err != nil {
		// A branch gone upstream is the Repos page's error, not this
		// turn's: the indexed head still answers, and a tag past it is
		// then reported as off the branch rather than as not yet indexed.
		return h, nil
	}
	h.Remote = remote
	return h, nil
}

// Depth is the poller's history depth.
func (g *Git) Depth() int { return g.depth }
