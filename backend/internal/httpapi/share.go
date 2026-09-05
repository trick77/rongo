package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
)

// Sharing: a thread readable by anyone holding the link.
//
// Two halves that do not resemble each other. The owner's half is four
// ordinary authenticated actions on a thread they already own. The public half
// is the only place in rongo that answers without a session at all, so
// everything it can reach is bounded by two things and nothing else: the token
// it was given, and the ceiling recorded with that token.
//
// The public handlers therefore never take a repo, a path, a thread id or a
// message id as an authorisation. They take a token, ask the store what that
// token covers, and refuse anything outside it.

// publicShare is what an anonymous reader gets: the thread as a record, and
// nothing about what it cost.
type publicShare struct {
	Title    string            `json:"title"`
	SharedAt string            `json:"shared_at"`
	Messages []threads.Message `json:"messages"`
}

// noindex marks a public response as something no crawler should keep. Set
// before anything else is written, so it is on the 404 as well: an indexed
// "not available" page is still a token in somebody's search results.
func noindex(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
}

// notFound is the one answer every public lookup gives for a token that is
// unknown, revoked, or whose thread is gone. Telling those apart would let
// someone guessing tokens learn which guesses were once real.
func notFound(w http.ResponseWriter) {
	http.Error(w, "no such share", http.StatusNotFound)
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	sh, err := s.deps.Threads.Share(r.Context(), u.Subject, id)
	s.writeShare(w, sh, err)
}

func (s *Server) handleShareUpdate(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	sh, err := s.deps.Threads.RaiseShare(r.Context(), u.Subject, id)
	s.writeShare(w, sh, err)
}

// writeShare answers the two endpoints that hand a link back. A thread with no
// turn, a thread that is not this reader's and a link that was never made are
// all 404: the record has nothing to share either way, and the three differ
// only in a detail that would say whose thread it is.
func (s *Server) writeShare(w http.ResponseWriter, sh threads.Share, err error) {
	switch {
	case err == nil:
	case errors.Is(err, threads.ErrNoShare):
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	case errors.Is(err, threads.ErrUnfinished):
		// 409, not 400: the request is well formed and will work in a moment.
		http.Error(w, "The last turn is still being answered.", http.StatusConflict)
		return
	default:
		slog.Error("share thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sh)
}

func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	revoked, err := s.deps.Threads.RevokeShare(r.Context(), u.Subject, id)
	if err != nil {
		slog.Error("revoke share failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !revoked {
		http.Error(w, "no such share", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleShares(w http.ResponseWriter, r *http.Request) {
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	list, err := s.deps.Threads.Shares(r.Context(), u.Subject)
	if err != nil {
		slog.Error("list shares failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

// handlePublicShare serves a shared thread to anyone holding the link.
//
// What it does NOT do is as much of the point as what it does: no pricing pass
// runs over the turns, so no token count, cost or model name leaves the
// process, and the follow-ups an answer offered are dropped because there is
// nothing here to ask them with. Calls and Scope are already `json:"-"` on the
// message itself.
func (s *Server) handlePublicShare(w http.ResponseWriter, r *http.Request) {
	noindex(w)
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return
	}
	sh, msgs, err := s.deps.Threads.SharedThread(r.Context(), r.PathValue("token"))
	if err != nil {
		if !errors.Is(err, threads.ErrNoShare) {
			slog.Error("read shared thread failed", "err", err)
		}
		notFound(w)
		return
	}
	for i := range msgs {
		msgs[i].Followups = nil
		msgs[i].Usage = nil
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(publicShare{
		Title:    sh.Title,
		SharedAt: sh.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		Messages: msgs,
	})
}

// handlePublicShareSource opens a cited file for a reader who has no session.
//
// The check is the feature. /api/source takes any repo/path/sha and is a
// reader for the whole indexed corpus; here the triple has to appear in a
// citation of a turn this link actually covers, or the answer is the same 404
// an unknown token gets. A reader can check every claim in front of them and
// reach nothing else.
func (s *Server) handlePublicShareSource(w http.ResponseWriter, r *http.Request) {
	noindex(w)
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	repo, path, sha := q.Get("repo"), q.Get("path"), q.Get("sha")
	cited, err := s.deps.Threads.SharedCitation(r.Context(), r.PathValue("token"), repo, path, sha)
	if err != nil {
		slog.Error("read shared citation failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !cited {
		notFound(w)
		return
	}
	s.serveSource(w, r, repo, path, sha)
}
