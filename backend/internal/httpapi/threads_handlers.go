package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
	"github.com/trick77/rongo/internal/usage"
)

// handleThreads answers one page of the reader's threads: the rail asks for
// 30, the Threads page for 50 at a time and then the page after, by cursor.
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	limit, ok := listLimit(w, r)
	if !ok {
		return
	}
	page, err := s.deps.Threads.ListPage(r.Context(), u.Subject, threads.ListOptions{
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
		// ?starred=true is the rail's starred section: every starred thread,
		// however old. Anything else is the plain list.
		StarredOnly: r.URL.Query().Get("starred") == "true",
	})
	if err != nil {
		slog.Error("list threads failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

// listLimit reads ?limit=: absent means the store's default, and a number
// that is not one, or is outside 1..MaxListLimit, is a 400 rather than
// silently another page size — the browser asked for something specific.
func listLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > threads.MaxListLimit {
		http.Error(w, "limit must be between 1 and 1000", http.StatusBadRequest)
		return 0, false
	}
	return n, true
}

// handleSearchThreads answers the Threads page's search box: every thread of
// the reader's whose title or messages match, title hits first.
func (s *Server) handleSearchThreads(w http.ResponseWriter, r *http.Request) {
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	limit, ok := listLimit(w, r)
	if !ok {
		return
	}
	hits, err := s.deps.Threads.Search(r.Context(), u.Subject, q, limit)
	if err != nil {
		slog.Error("search threads failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Items []threads.Hit `json:"items"`
	}{Items: hits})
}

func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	// Owns before Messages, unlike the routes below, which carry the owner
	// inside their own statement. Messages answers an empty list for a thread
	// that is not this reader's, and an empty list is a 200 — so without this
	// the one route that reads a thread would tell "no such address" and
	// "exists, but not yours" apart, while every other one answers 404 to both.
	owns, err := s.deps.Threads.Owns(r.Context(), u.Subject, id)
	if err != nil {
		slog.Error("check thread owner failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	msgs, err := s.deps.Threads.Messages(r.Context(), u.Subject, id)
	if err != nil {
		slog.Error("read thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// Priced here, from the current table, never from a stored figure: the
	// record holds tokens, and the price is configuration that can change.
	// A turn with no calls on record (older than the table, or nothing paid)
	// carries no usage rather than an empty one, so the browser shows nothing
	// instead of a zero.
	for i := range msgs {
		if len(msgs[i].Calls) > 0 {
			report := usage.Price(msgs[i].Calls)
			msgs[i].Usage = &report
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}

// handleThreadSummary is one thread's row — its title, for the header of a
// thread the rail's page does not carry. Not the reader's → 404, like every
// other read by address.
func (s *Server) handleThreadSummary(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	t, found, err := s.deps.Threads.Get(r.Context(), u.Subject, id)
	if err != nil {
		slog.Error("read thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(t)
}

// threadRequest is what the rail's own actions send: a rename carries the new
// title, a delete carries nothing.
type threadRequest struct {
	Title string `json:"title"`
}

// maxTitleRunes is what a thread title may be, matching the length the store
// cuts its placeholder to.
const maxTitleRunes = 48

func (s *Server) handleRenameThread(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	var req threadRequest
	// Capped like every other body this package reads: a title is a line of
	// text, and one that is not would be buffered here and then shipped with
	// the list on every load of the rail.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	// The same length the placeholder is cut to, so a typed title cannot
	// outgrow the one the model writes. Refused rather than truncated: the
	// rail would otherwise show something nobody asked for.
	if len([]rune(title)) > maxTitleRunes {
		http.Error(w, "title is too long", http.StatusBadRequest)
		return
	}
	renamed, err := s.deps.Threads.Rename(r.Context(), u.Subject, id, title)
	if err != nil {
		slog.Error("rename thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !renamed {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	deleted, err := s.deps.Threads.Delete(r.Context(), u.Subject, id)
	if err != nil {
		slog.Error("delete thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	// Everything else the thread left behind, and only once the delete has
	// reported a row — so a 404 for someone else's thread cannot be used to
	// cut their answer short or re-pin their conversation.
	//
	// The rows are gone by now, through the schema's cascades. What is left is
	// a turn that may still be streaming into them, which would run its model
	// calls to completion and be paid for.
	s.turns.cancel(id)
	w.WriteHeader(http.StatusNoContent)
}

// handleStarThread and handleUnstarThread are ../loom's pair: two verbs
// rather than a PATCH body field, so a rename's "title is required" stays
// what it is. No body, 204 like rename and delete — the browser knows what
// it asked for and patches its own row.
func (s *Server) handleStarThread(w http.ResponseWriter, r *http.Request) {
	s.handleSetThreadStarred(w, r, true)
}

func (s *Server) handleUnstarThread(w http.ResponseWriter, r *http.Request) {
	s.handleSetThreadStarred(w, r, false)
}

func (s *Server) handleSetThreadStarred(w http.ResponseWriter, r *http.Request, starred bool) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	found, err := s.deps.Threads.SetStarred(r.Context(), u.Subject, id, starred)
	if err != nil {
		slog.Error("star thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// threadTarget resolves the reader and the thread id both single-thread
// actions need, answering the request itself when either is missing. A thread
// that is not this reader's is never told apart from one that is gone: both
// end as the 404 the handlers write once the store reports no row.
func (s *Server) threadTarget(w http.ResponseWriter, r *http.Request) (auth.User, int64, bool) {
	if s.deps.Threads == nil {
		http.Error(w, "threads unavailable", http.StatusServiceUnavailable)
		return auth.User{}, 0, false
	}
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return auth.User{}, 0, false
	}
	// An address that names no thread is a 404 and not a 400: to the reader
	// there is no difference between a thread that never existed, one that was
	// deleted, and one that is someone else's, and the handlers below keep it
	// that way by pairing the id with an ownership predicate.
	id, ok, err := s.deps.Threads.Resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		slog.Error("resolve thread failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return auth.User{}, 0, false
	}
	if !ok {
		http.Error(w, "no such thread", http.StatusNotFound)
		return auth.User{}, 0, false
	}
	return u, id, true
}
