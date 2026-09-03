// Package sourceview serves a cited file out of rongo's own checkout, so a
// reader can open a source without a forge. The file is read at the commit
// the citation was written from, never from the working tree: a branch that
// has moved on since would put the highlighted lines on different code.
package sourceview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/trick77/rongo/internal/repos"
)

// File is one source file at one commit.
type File struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Content string `json:"content"`
}

var (
	// ErrNotFound covers an unknown repository, a path that does not exist at
	// the commit, and a checkout that is not on disk. Which one it was is in
	// the wrapped message for the log, not for the caller: all three mean
	// "rongo cannot show this file".
	ErrNotFound = errors.New("source not found")
	// ErrInvalid is a request rongo will not even try: an empty path, a path
	// that climbs out of the tree, or a commit that is not a commit.
	ErrInvalid = errors.New("invalid source request")
	// ErrBinary is a file that is not text; there are no lines to show.
	ErrBinary = errors.New("binary file")
	// ErrTooLarge is a file above maxBytes.
	ErrTooLarge = errors.New("file too large to show")
)

// maxBytes caps what the viewer will render. A generated or vendored file of
// several megabytes is not something a person reads line by line, and it would
// hold the answer page hostage while it loads.
const maxBytes = 1 << 20

// shaRe is what reaches git as the left side of "sha:path". Anything else is
// refused before it is an argument: a "commit" starting with a dash would be
// read by git as an option.
var shaRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// FileReader reads a path at a commit out of a checkout. *gitrepo.Client
// satisfies it.
type FileReader interface {
	ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error)
}

// Service resolves a citation to file content.
type Service struct {
	db  *sql.DB
	git FileReader
}

// New builds a Service over the index database and the checkouts.
func New(db *sql.DB, git FileReader) *Service {
	return &Service{db: db, git: git}
}

// Read returns path in repo at sha. An empty sha means "the commit the file
// was last indexed at" — citations recorded before the commit travelled with
// them have none — and, failing a files row, the repository's last indexed
// commit.
func (s *Service) Read(ctx context.Context, repo, path, sha string) (File, error) {
	if err := validatePath(path); err != nil {
		return File{}, err
	}
	if sha != "" && !shaRe.MatchString(sha) {
		return File{}, fmt.Errorf("%w: commit %q", ErrInvalid, sha)
	}

	var branch, lastSHA string
	err := s.db.QueryRowContext(ctx,
		`SELECT branch, last_sha FROM repo_state WHERE name = ?`, repo).Scan(&branch, &lastSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, fmt.Errorf("%w: unknown repository %q", ErrNotFound, repo)
	}
	if err != nil {
		return File{}, fmt.Errorf("look up repository %q: %w", repo, err)
	}

	if sha == "" {
		err := s.db.QueryRowContext(ctx,
			`SELECT sha FROM files WHERE repo = ? AND path = ?`, repo, path).Scan(&sha)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return File{}, fmt.Errorf("look up %s/%s: %w", repo, path, err)
		}
		if sha == "" {
			sha = lastSHA
		}
		if sha == "" {
			return File{}, fmt.Errorf("%w: %s has not been indexed", ErrNotFound, repo)
		}
	}

	// The spec carries only the name: that is all Dir needs, and the name
	// came from repo_state, so it cannot point outside the checkout root.
	body, err := s.git.ReadFile(ctx, repos.Spec{Name: repo}, sha, path)
	if err != nil {
		return File{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	if len(body) > maxBytes {
		return File{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(body))
	}
	if !utf8.Valid(body) {
		return File{}, ErrBinary
	}
	return File{Repo: repo, Branch: branch, Path: path, SHA: sha, Content: string(body)}, nil
}

// validatePath refuses what git would misread or what would leave the tree.
// git show resolves "sha:path" inside the object store, so ".." cannot
// actually escape, but a path rongo never indexed is a request rongo has no
// business making.
func validatePath(path string) error {
	switch {
	case path == "":
		return fmt.Errorf("%w: empty path", ErrInvalid)
	case strings.HasPrefix(path, "/"), strings.HasPrefix(path, "-"):
		return fmt.Errorf("%w: path %q", ErrInvalid, path)
	case strings.ContainsRune(path, 0):
		return fmt.Errorf("%w: path contains NUL", ErrInvalid)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: path %q", ErrInvalid, path)
		}
	}
	return nil
}
