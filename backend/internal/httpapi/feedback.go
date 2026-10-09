package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/trick77/rongo/internal/threads"
)

// feedbackRequest is the verdict the reader clicked. The answers it covers are
// not part of it: the server pins the verdict to the newest finished answer,
// whatever the browser had loaded.
type feedbackRequest struct {
	Verdict int    `json:"verdict"`
	Reason  string `json:"reason"`
}

// handleGetFeedback answers the reader's verdict on their thread, or null when
// none was given. Not the reader's → 404, like every other read by address.
func (s *Server) handleGetFeedback(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	owns, err := s.deps.Threads.Owns(r.Context(), u.Subject, id)
	if err != nil {
		serverError(w, "check thread owner failed", err)
		return
	}
	if !owns {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	fb, found, err := s.deps.Threads.Feedback(r.Context(), u.Subject, id)
	if err != nil {
		serverError(w, "read feedback failed", err)
		return
	}
	var body *threads.Feedback
	if found {
		body = &fb
	}
	writeJSON(w, body)
}

// handlePutFeedback stores the verdict, replacing any earlier one. A thread
// with no finished answer has nothing to judge and answers 404 with the ones
// that are not the reader's.
func (s *Server) handlePutFeedback(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	var req feedbackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if req.Verdict != 1 && req.Verdict != -1 {
		http.Error(w, "verdict must be 1 or -1", http.StatusBadRequest)
		return
	}
	// A reason says what was off, so only a thumbs down carries one.
	if !threads.ValidFeedbackReason(req.Reason) || (req.Verdict == 1 && req.Reason != "") {
		http.Error(w, "unknown reason", http.StatusBadRequest)
		return
	}
	found, err := s.deps.Threads.SetFeedback(r.Context(), u.Subject, id, req.Verdict, req.Reason)
	if err != nil {
		serverError(w, "set feedback failed", err)
		return
	}
	if !found {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	fb, _, err := s.deps.Threads.Feedback(r.Context(), u.Subject, id)
	if err != nil {
		serverError(w, "read feedback failed", err)
		return
	}
	writeJSON(w, fb)
}

// handleDeleteFeedback takes the verdict back.
func (s *Server) handleDeleteFeedback(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	found, err := s.deps.Threads.ClearFeedback(r.Context(), u.Subject, id)
	if err != nil {
		serverError(w, "clear feedback failed", err)
		return
	}
	if !found {
		http.Error(w, "no such thread", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
