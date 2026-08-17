// Package gitrepo drives the git binary. rongo clones and owns its checkouts
// rather than reading through a forge API: an API cannot grep, per-file fetches
// rate-limit at this corpus size, and the "why is it like this" pipeline needs
// local history.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/trick77/rongo/internal/repos"
)

// ErrBranchGone reports that a configured branch does not exist on the remote.
// It is a named error because the caller must surface it loudly: a silent stop
// freezes the index while every status looks healthy, and answers then come
// from months-old code.
var ErrBranchGone = errors.New("configured branch not found")

// Client runs git commands against checkouts under root.
type Client struct {
	git  string
	root string
}

// New builds a Client. gitBin comes from exttools.Resolve, which has already
// verified the binary exists.
func New(gitBin, root string) *Client {
	return &Client{git: gitBin, root: root}
}

// Dir is where a repository's checkout lives. repos.Load has already validated
// that Name is a single path segment, so this cannot escape root.
func (c *Client) Dir(spec repos.Spec) string {
	return filepath.Join(c.root, spec.Name)
}

// EnsureCloned clones the repository if it is not present. The clone keeps full
// history because the "why" pipeline reads it, and source is cheap on disk.
func (c *Client) EnsureCloned(ctx context.Context, spec repos.Spec, token string) error {
	dir := c.Dir(spec)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return fmt.Errorf("create repository root: %w", err)
	}
	_, err := c.run(ctx, c.root, "clone", "--quiet", authURL(spec.CloneURL, token), dir)
	return err
}

// DefaultBranch asks the remote which branch it considers default. Never assume
// master: this corpus mixes master and main, and the repositories on main are
// exactly the third-party ones the cross-repo logic needs.
func (c *Client) DefaultBranch(ctx context.Context, spec repos.Spec) (string, error) {
	out, err := c.run(ctx, c.root, "ls-remote", "--symref", spec.CloneURL, "HEAD")
	if err != nil {
		return "", err
	}
	// The symref line reads: "ref: refs/heads/main\tHEAD"
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "ref:") {
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 2 {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}
	return "", fmt.Errorf("%s: remote reported no default branch", spec.Name)
}

// Fetch updates the remote-tracking refs.
func (c *Client) Fetch(ctx context.Context, spec repos.Spec, token string) error {
	_, err := c.run(ctx, c.Dir(spec), "fetch", "--quiet", "--prune",
		authURL(spec.CloneURL, token), "+refs/heads/*:refs/remotes/origin/*")
	return err
}

// HeadSHA returns the commit the branch points at on the remote-tracking side.
// A missing branch yields ErrBranchGone so the poller can tell "branch deleted"
// apart from "network hiccup".
func (c *Client) HeadSHA(ctx context.Context, spec repos.Spec, branch string) (string, error) {
	out, err := c.run(ctx, c.Dir(spec), "rev-parse", "--verify", "--quiet",
		"refs/remotes/origin/"+branch)
	if err != nil || strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%s: branch %q: %w", spec.Name, branch, ErrBranchGone)
	}
	return strings.TrimSpace(out), nil
}

// ChangedPaths lists the paths differing between two commits. This is what
// keeps a push from costing a full re-index. It stays valid across a branch
// change too, because both commits live in the same object store.
func (c *Client) ChangedPaths(ctx context.Context, spec repos.Spec, fromSHA, toSHA string) ([]string, error) {
	out, err := c.run(ctx, c.Dir(spec), "diff", "--name-only", fromSHA+".."+toSHA)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// Change is one path in a diff, and whether the newer commit still has it.
type Change struct {
	Path    string
	Deleted bool
}

// ChangedEntries is ChangedPaths with the delete/modify distinction the indexer
// needs: a deleted path must have its rows removed, a modified one re-read.
//
// --name-only cannot tell the two apart, and treating a failed read as a
// deletion would conflate a broken checkout with code that is genuinely gone —
// the index would quietly drop files that still exist. --no-renames is
// deliberate: to an indexer a rename IS a delete plus an add, and asking git to
// detect renames only produces a three-field record to parse for the same
// outcome.
func (c *Client) ChangedEntries(ctx context.Context, spec repos.Spec, fromSHA, toSHA string) ([]Change, error) {
	out, err := c.run(ctx, c.Dir(spec), "diff", "--name-status", "--no-renames", fromSHA+".."+toSHA)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, line := range nonEmptyLines(out) {
		status, path, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("unparseable diff record %q", line)
		}
		changes = append(changes, Change{
			Path:    strings.TrimSpace(path),
			Deleted: strings.HasPrefix(status, "D"),
		})
	}
	return changes, nil
}

// ListPaths lists every tracked path at a commit, for the initial full index.
func (c *Client) ListPaths(ctx context.Context, spec repos.Spec, sha string) ([]string, error) {
	out, err := c.run(ctx, c.Dir(spec), "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// ReadFile reads one path at one commit. Reading from the commit rather than
// the working tree keeps every chunk attributable to an exact SHA, which is
// what makes a citation verifiable later.
func (c *Client) ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.git, "show", sha+":"+path)
	cmd.Dir = c.Dir(spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read %s at %s: %w: %s", path, shortSHA(sha), err,
			redact(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (c *Client) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.git, args...)
	cmd.Dir = dir
	// Never let git prompt: a hung credential prompt would stall the poller
	// forever with no output to diagnose it.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// redact keeps an injected token out of an error that will be logged.
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err,
			redact(stderr.String()))
	}
	return stdout.String(), nil
}

// authURL injects the token into an https remote for the duration of one
// command. It is never written to disk and never logged.
func authURL(raw, token string) string {
	if token == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return raw
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

// credentialInURL matches the userinfo segment of any URL-shaped substring:
// a scheme, "://", everything up to an "@" that is not a slash or whitespace.
var credentialInURL = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s]+@`)

// redact strips the userinfo from anything URL-shaped in text about to be
// logged. git quotes the remote it failed against, and that remote carries the
// token injected by authURL.
//
// This deliberately works on the raw string rather than splitting into fields
// and calling url.Parse: git wraps the remote in single quotes and often
// appends a colon, so the field is not a parseable URL and url.Parse returns an
// error with a nil User. An earlier version did exactly that and leaked the
// token through all three of git's common authentication-failure messages —
// see TestRedact_realisticGitErrorShapes, which pins every one of them.
func redact(s string) string {
	return credentialInURL.ReplaceAllString(s, "${1}REDACTED@")
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
