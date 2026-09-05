package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/trick77/rongo/internal/sourceview"
)

// SourceReader serves a cited file out of rongo's own checkout.
// *sourceview.Service satisfies it.
type SourceReader interface {
	Read(ctx context.Context, repo, path, sha string) (sourceview.File, error)
}

// handleSource answers GET /api/source?repo=&path=&sha= with the file a
// citation points at, read at the cited commit. It is what makes a source in
// the evidence panel something a reader can open rather than only read about.
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.serveSource(w, r, q.Get("repo"), q.Get("path"), q.Get("sha"))
}

// serveSource reads one file and writes it, or writes the reason it cannot.
// Both the signed-in route above and the share-scoped one in share.go end
// here, so a reader following a citation gets the same file and the same
// message whichever door they came through.
func (s *Server) serveSource(w http.ResponseWriter, r *http.Request, repo, path, sha string) {
	if s.deps.Source == nil {
		http.Error(w, "source view unavailable", http.StatusServiceUnavailable)
		return
	}
	f, err := s.deps.Source.Read(r.Context(), repo, path, sha)
	switch {
	case err == nil:
	case errors.Is(err, sourceview.ErrInvalid):
		http.Error(w, "malformed source request", http.StatusBadRequest)
		return
	case errors.Is(err, sourceview.ErrNotFound):
		// The detail (unknown repository, path gone at that commit, no checkout)
		// is for the log. The reader learns that rongo cannot show the file.
		slog.Info("source not found", "repo", repo, "path", path, "sha", sha, "err", err)
		http.Error(w, "This file is not in rongo's checkout at the cited commit.", http.StatusNotFound)
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
