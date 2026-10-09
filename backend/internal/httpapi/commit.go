package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/sourceview"
)

// CommitReader serves a cited commit out of rongo's own checkout.
// *sourceview.Service satisfies it.
type CommitReader interface {
	Commit(ctx context.Context, repo, sha string) (sourceview.Commit, error)
	// RecordedCommit is Commit for a commit a turn cites: one the lane has
	// dropped since is still served.
	RecordedCommit(ctx context.Context, repo, sha string) (sourceview.Commit, error)
}

// handleCommit answers GET /api/commit?repo=&sha= with the commit a
// changes answer cites: message, date, and the files it touched. It is to a
// commit citation what handleSource is to a file citation, down to the
// record being asked only when the lane no longer holds the commit.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, sha := q.Get("repo"), q.Get("sha")
	var cited citedFunc
	if u, ok := auth.UserFrom(r.Context()); ok && s.deps.Threads != nil {
		cited = func(ctx context.Context) (bool, error) {
			return s.deps.Threads.CommitCitedBy(ctx, u.Subject, repo, sha)
		}
	}
	s.serveCommit(w, r, repo, sha, cited)
}

// handlePublicShareCommit is handleCommit for a reader with no session: the
// pair has to be cited by a turn the link covers, or it is the same 404 an
// unknown token gets. Same reasoning as handlePublicShareSource.
func (s *Server) handlePublicShareCommit(w http.ResponseWriter, r *http.Request) {
	noindex(w)
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	repo, sha := q.Get("repo"), q.Get("sha")
	cited, err := s.deps.Threads.SharedCommit(r.Context(), r.PathValue("token"), repo, sha)
	if err != nil {
		serverError(w, "read shared commit citation failed", err)
		return
	}
	if !cited {
		notFound(w)
		return
	}
	s.serveCommit(w, r, repo, sha, alwaysCited)
}

func (s *Server) serveCommit(w http.ResponseWriter, r *http.Request, repo, sha string, cited citedFunc) {
	if s.deps.Commit == nil {
		http.Error(w, "commit view unavailable", http.StatusServiceUnavailable)
		return
	}
	c, err := s.deps.Commit.Commit(r.Context(), repo, sha)
	if errors.Is(err, sourceview.ErrNotInLane) && cited != nil {
		ok, cerr := cited(r.Context())
		if cerr != nil {
			serverError(w, "read commit citation failed", cerr)
			return
		}
		if ok {
			c, err = s.deps.Commit.RecordedCommit(r.Context(), repo, sha)
		}
	}
	switch {
	case err == nil:
	case errors.Is(err, sourceview.ErrInvalid):
		http.Error(w, "malformed commit request", http.StatusBadRequest)
		return
	case errors.Is(err, sourceview.ErrNotFound):
		slog.Info("commit not found", "repo", repo, "sha", sha, "err", err)
		http.Error(w, "This commit is not in Rongo's checkout.", http.StatusNotFound)
		return
	default:
		serverError(w, "read commit failed", err)
		return
	}
	writeJSON(w, c)
}
