package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

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
	cited := s.ownerCited(r, func(ctx context.Context, subject string) (bool, error) {
		return s.deps.Threads.CommitCitedBy(ctx, subject, repo, sha)
	})
	s.serveCommit(w, r, repo, sha, cited)
}

// handlePublicShareCommit is handleCommit for a reader with no session: the
// pair has to be cited by a turn the link covers, or it is the same 404 an
// unknown token gets. Same reasoning as handlePublicShareSource.
func (s *Server) handlePublicShareCommit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, sha := q.Get("repo"), q.Get("sha")
	ok := s.sharedCited(w, r, "read shared commit citation failed", func(ctx context.Context, token string) (bool, error) {
		return s.deps.Threads.SharedCommit(ctx, token, repo, sha)
	})
	if !ok {
		return
	}
	s.serveCommit(w, r, repo, sha, alwaysCited)
}

func (s *Server) serveCommit(w http.ResponseWriter, r *http.Request, repo, sha string, cited citedFunc) {
	if s.deps.Commit == nil {
		http.Error(w, "commit view unavailable", http.StatusServiceUnavailable)
		return
	}
	serveCited(w, r, cited, citedView[sourceview.Commit]{
		read: func(ctx context.Context) (sourceview.Commit, error) {
			return s.deps.Commit.Commit(ctx, repo, sha)
		},
		readRecorded: func(ctx context.Context) (sourceview.Commit, error) {
			return s.deps.Commit.RecordedCommit(ctx, repo, sha)
		},
		fromRecord:  func(err error) bool { return errors.Is(err, sourceview.ErrNotInLane) },
		citationLog: "read commit citation failed",
		readLog:     "read commit failed",
		statuses: []errStatus{
			{err: sourceview.ErrInvalid, status: http.StatusBadRequest, body: "malformed commit request"},
			{err: sourceview.ErrNotFound, status: http.StatusNotFound, body: "This commit is not in Rongo's checkout.",
				log: func(err error) { slog.Info("commit not found", "repo", repo, "sha", sha, "err", err) }},
		},
	})
}
