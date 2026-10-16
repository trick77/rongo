package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/ship/kit/httpx"
)

// writeJSON encodes v as the response body, never cached (kit's WriteJSON).
func writeJSON(w http.ResponseWriter, v any) {
	httpx.WriteJSON(w, http.StatusOK, v)
}

// writeJSONStatus is writeJSON under another status line.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	httpx.WriteJSON(w, status, v)
}

// serverError logs the cause and answers 500 with a body that says nothing
// about it. Not internalError (ask.go): that one reads the request context to
// log a cancelled turn at Warn, and these handlers have no turn to cancel.
func serverError(w http.ResponseWriter, msg string, err error, attrs ...any) {
	slog.Error(msg, append([]any{"err", err}, attrs...)...)
	httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
}

// requireUser is the signed-in reader, or a 401 already written. requireAuth
// in front of every route makes the 401 unreachable; it stays so a handler
// mounted without it fails closed.
func requireUser(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
	}
	return u, ok
}

// reader is requireUser for a route that reads or writes the thread record:
// no record is a 503 before anyone is asked who they are.
func (s *Server) reader(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	if s.deps.Threads == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "threads unavailable")
		return auth.User{}, false
	}
	return requireUser(w, r)
}

// decodeJSON reads a request body into v, or answers 400. Every body this
// package reads is capped at a megabyte: the largest is a question with its
// pasted texts, and the rest are a line of text or a verdict.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "malformed request")
		return false
	}
	return true
}

// actionOutcome answers for a store call that reports whether it found a row:
// an error is a 500 logged under logMsg, no row is a 404 with notFoundMsg, and
// a row is the caller's to finish. A thread that is not this reader's and one
// that is gone are the same "no row", so the 404 never tells them apart.
func actionOutcome(w http.ResponseWriter, found bool, err error, logMsg, notFoundMsg string) bool {
	if err != nil {
		serverError(w, logMsg, err)
		return false
	}
	if !found {
		httpx.WriteError(w, http.StatusNotFound, notFoundMsg)
		return false
	}
	return true
}

// threadAction is one owner's action on one thread that answers with no body:
// resolve the reader and the thread, run the store call, 204 when it found
// the row. The handlers whose work is only that call go through here.
func (s *Server) threadAction(w http.ResponseWriter, r *http.Request, logMsg, notFoundMsg string,
	fn func(ctx context.Context, subject string, id int64) (bool, error)) {

	u, id, ok := s.threadTarget(w, r)
	if !ok {
		return
	}
	found, err := fn(r.Context(), u.Subject, id)
	if !actionOutcome(w, found, err, logMsg, notFoundMsg) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
