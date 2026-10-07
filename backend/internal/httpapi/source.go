package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/sourceview"
)

// SourceReader serves a cited file out of rongo's own checkout.
// *sourceview.Service satisfies it.
type SourceReader interface {
	Read(ctx context.Context, repo, path, sha string) (sourceview.File, error)
	// ReadRecorded is Read for a file a turn cites: a path the index has
	// dropped since is still served at the cited commit.
	ReadRecorded(ctx context.Context, repo, path, sha string) (sourceview.File, error)
}

// handleSource answers GET /api/source?repo=&path=&sha= with the file a
// citation points at, read at the cited commit. It is what makes a source in
// the evidence panel something a reader can open rather than only read about.
//
// The route takes any triple, so it reads as the record only for one the
// reader's own threads cite. Anything else needs a files row: without that
// rule a signed-in reader could open a secret manifest at a commit from
// before the index ran.
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, path, sha := q.Get("repo"), q.Get("path"), q.Get("sha")
	recorded := false
	if u, ok := auth.UserFrom(r.Context()); ok && sha != "" && s.deps.Source != nil && s.deps.Threads != nil {
		cited, err := s.deps.Threads.CitedBy(r.Context(), u.Subject, repo, path, sha)
		if err != nil {
			slog.Error("read owner citation failed", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		recorded = cited
	}
	s.serveSource(w, r, repo, path, sha, recorded)
}

// serveSource reads one file and writes it, or writes the reason it cannot.
// Both the signed-in route above and the share-scoped one in share.go end
// here, so a reader following a citation gets the same file and the same
// message whichever door they came through. recorded means a turn the caller
// may see cites exactly this triple, so a path gone from the index since is
// still read at that commit.
func (s *Server) serveSource(w http.ResponseWriter, r *http.Request, repo, path, sha string, recorded bool) {
	if s.deps.Source == nil {
		http.Error(w, "source view unavailable", http.StatusServiceUnavailable)
		return
	}
	read := s.deps.Source.Read
	if recorded {
		read = s.deps.Source.ReadRecorded
	}
	f, err := read(r.Context(), repo, path, sha)
	switch {
	case err == nil:
	case errors.Is(err, sourceview.ErrInvalid):
		http.Error(w, "malformed source request", http.StatusBadRequest)
		return
	case errors.Is(err, sourceview.ErrNotFound):
		// The detail (unknown repository, path gone at that commit, no checkout)
		// is for the log. The reader learns that rongo cannot show the file.
		slog.Info("source not found", "repo", repo, "path", path, "sha", sha, "err", err)
		http.Error(w, "This file is not in Rongo's checkout at the cited commit.", http.StatusNotFound)
		return
	case errors.Is(err, sourceview.ErrBinary):
		http.Error(w, "This is a binary file; there are no lines to show.", http.StatusUnsupportedMediaType)
		return
	case errors.Is(err, sourceview.ErrTooLarge):
		http.Error(w, "This file is too large to show here.", http.StatusRequestEntityTooLarge)
		return
	default:
		slog.Error("read source failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f)
}
