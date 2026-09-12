package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/usage"
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
// one figure for what the whole of it cost. The total is two flat fields
// rather than a usage.Report, because a Report carries the per-call breakdown
// and the model names, and neither is the reader's business. Both are absent
// when the thread paid for nothing (no usage, not a zero); the cost alone is
// absent when no price table is loaded (tokens only, the same rule as the
// owner's view).
type publicShare struct {
	Title       string            `json:"title"`
	SharedAt    string            `json:"shared_at"`
	Messages    []threads.Message `json:"messages"`
	TotalTokens *int              `json:"total_tokens,omitempty"`
	CostUSD     *float64          `json:"cost_usd,omitempty"`
}

// noindex marks a public response as something no crawler and no cache should
// keep. Set before anything else is written, so it is on the 404 as well: an
// indexed "not available" page is still a token in somebody's search results.
//
// no-store goes with it, as it does on the served shell: the answer to a
// capability URL is private data, and a shared cache in front that kept it
// would go on serving a thread after its link was revoked.
func noindex(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")
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
		// It only reaches here when NOTHING in the thread has finished — a
		// thread with any finished turn freezes below the one in flight.
		http.Error(w, "This thread's first answer is still being written.", http.StatusConflict)
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
// What it does NOT do is as much of the point as what it does: the pricing
// pass runs once over the thread as a whole, so the reader gets one token
// count and one cost, and no per-turn figure or model name leaves the process.
// The follow-ups an answer offered are dropped because there is nothing here
// to ask them with. Calls and Scope are already `json:"-"` on the message
// itself.
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
	// Every call the shared turns paid for, in one list: the ceiling is
	// already applied, so summing what arrived is the total the link covers.
	// Priced from the current table, never a stored figure, as the owner's
	// read does.
	var calls []usage.Call
	for i := range msgs {
		calls = append(calls, msgs[i].Calls...)
		msgs[i].Followups = nil
		msgs[i].Usage = nil
		// The timeline goes with the per-turn usage: how long each step took
		// and what the pipeline is made of is the same class of thing as what
		// one turn cost, and a link's audience was sent an answer, not a
		// machine room.
		msgs[i].Steps = nil
	}
	out := publicShare{
		Title:    sh.Title,
		SharedAt: sh.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		Messages: msgs,
	}
	if len(calls) > 0 {
		report := usage.Price(calls)
		out.TotalTokens = &report.Total
		out.CostUSD = report.CostUSD
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// shareTitle is what the SPA shell puts in a link preview's og:title, so a
// share link unfurls in Slack or X as the question it answers rather than as a
// bare URL. It reports false for a token that is unknown or revoked, which is
// the same thing handlePublicShare above already says with a 404 — the shell
// tells a crawler nothing the public API does not.
//
// It runs on every page view of a share link, not once per crawler, so it
// reads the title alone rather than the whole thread: a browser opening the
// link would otherwise read every turn twice, once for the HTML and once for
// the JSON the SPA then fetches.
func (s *Server) shareTitle(ctx context.Context, token string) (string, bool) {
	if s.deps.Threads == nil {
		return "", false
	}
	title, err := s.deps.Threads.SharedTitle(ctx, token)
	if err != nil {
		// A token that is not live is the ordinary case and says nothing. A
		// database that cannot answer is not: without this line every link on
		// the estate quietly unfurls as the site card and the log is silent
		// about why.
		if !errors.Is(err, threads.ErrNoShare) {
			slog.Error("read shared title failed", "err", err)
		}
		return "", false
	}
	return title, true
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
