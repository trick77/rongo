// Package sourceview serves a cited file out of rongo's own checkout, so a
// reader can open a source without a forge. The file is read at the commit
// the citation was written from, never from the working tree: a branch that
// has moved on since would put the highlighted lines on different code.
//
// It serves only what the index serves. A file the indexer refused — secret,
// vendored, too large — never reached an answer, and this is not a second
// door to it: a signed-in reader gets the evidence behind answers, not a
// file browser over every commit of every repository.
package sourceview

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

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
	// ErrNotFound covers an unknown repository, a path the index does not
	// serve, a path that does not exist at the commit, and a checkout that is
	// not on disk. Which one it was is in the wrapped message for the log,
	// not for the caller: all of them mean "rongo cannot show this file".
	ErrNotFound = errors.New("source not found")
	// ErrInvalid is a request rongo will not even try: an empty path, a path
	// that climbs out of the tree, or a commit that is not a commit.
	ErrInvalid = errors.New("invalid source request")
	// ErrBinary is a file that is not text; there are no lines to show.
	ErrBinary = errors.New("binary file")
	// ErrTooLarge is a file above the size cap.
	ErrTooLarge = errors.New("file too large to show")
)

// shaRe is what reaches git as the left side of "sha:path". Anything else is
// refused before it is an argument: a "commit" starting with a dash would be
// read by git as an option.
var shaRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// FileReader is the checkout as this package needs it. *gitrepo.Client
// satisfies it.
type FileReader interface {
	// Object reports the kind ("blob", "tree", ...) and size of sha:path
	// without reading it.
	Object(ctx context.Context, spec repos.Spec, sha, path string) (kind string, size int64, err error)
	ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error)
}

// Service resolves a citation to file content.
type Service struct {
	db       *sql.DB
	git      FileReader
	maxBytes int64
}

// New builds a Service over the index database and the checkouts. maxBytes
// is the index's own ceiling (BACKEND_INDEX_MAX_FILE_BYTES): a file the
// indexer took, the viewer shows; one it skipped as too large, the viewer
// would never be asked for.
func New(db *sql.DB, git FileReader, maxBytes int) *Service {
	return &Service{db: db, git: git, maxBytes: int64(maxBytes)}
}

// Read returns path in repo at sha. An empty sha means "the commit the file
// was last indexed at" — citations recorded before the commit travelled with
// them have none.
func (s *Service) Read(ctx context.Context, repo, path, sha string) (File, error) {
	if err := validatePath(path); err != nil {
		return File{}, err
	}
	if sha != "" && !shaRe.MatchString(sha) {
		return File{}, fmt.Errorf("%w: commit %q", ErrInvalid, sha)
	}

	var branch string
	err := s.db.QueryRowContext(ctx,
		`SELECT branch FROM repo_state WHERE name = ?`, repo).Scan(&branch)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, fmt.Errorf("%w: unknown repository %q", ErrNotFound, repo)
	}
	if err != nil {
		return File{}, fmt.Errorf("look up repository %q: %w", repo, err)
	}

	// The files row is the permission. Only a path the indexer took is
	// served, at whatever commit: a secret the selector refused has no row
	// worth serving, and neither has a path the index never saw.
	var indexedSHA, skipReason string
	err = s.db.QueryRowContext(ctx,
		`SELECT sha, skip_reason FROM files WHERE repo = ? AND path = ?`, repo, path).Scan(&indexedSHA, &skipReason)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, fmt.Errorf("%w: %s/%s is not indexed", ErrNotFound, repo, path)
	}
	if err != nil {
		return File{}, fmt.Errorf("look up %s/%s: %w", repo, path, err)
	}
	if skipReason != "" {
		return File{}, fmt.Errorf("%w: %s/%s was not indexed (%s)", ErrNotFound, repo, path, skipReason)
	}
	if sha == "" {
		sha = indexedSHA
	}

	// The spec carries only the name: that is all Dir needs, and the name
	// came from repo_state, so it cannot point outside the checkout root.
	spec := repos.Spec{Name: repo}
	kind, size, err := s.git.Object(ctx, spec, sha, path)
	if err != nil {
		return File{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	if kind != "blob" {
		// "git show sha:dir" prints a listing and exits 0; it is not a file.
		return File{}, fmt.Errorf("%w: %s/%s is a %s, not a file", ErrNotFound, repo, path, kind)
	}
	if size > s.maxBytes {
		// Refused before the read: ReadFile would buffer the whole object.
		return File{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, size)
	}
	body, err := s.git.ReadFile(ctx, spec, sha, path)
	if err != nil {
		return File{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	// The same verdict the indexer gives (indexer.isBinary): a NUL byte. Not
	// utf8.Valid — a Latin-1 comment is text the indexer took and cited, and
	// the viewer must not refuse what the answer rests on.
	if bytes.IndexByte(body, 0) >= 0 {
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
