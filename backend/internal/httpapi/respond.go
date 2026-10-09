package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/trick77/rongo/internal/auth"
)

// writeJSON encodes v as the response body. The encoder's error is dropped on
// purpose: by the time it fails the status line is out, and there is nothing
// left to tell the client.
func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

// writeJSONStatus is writeJSON under another status line. The header goes
// before WriteHeader, which is why a handler cannot set the status itself and
// then call writeJSON.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// serverError logs the cause and answers 500 with a body that says nothing
// about it. Not internalError (ask.go): that one reads the request context to
// log a cancelled turn at Warn, and these handlers have no turn to cancel.
func serverError(w http.ResponseWriter, msg string, err error, attrs ...any) {
	slog.Error(msg, append([]any{"err", err}, attrs...)...)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// requireUser is the signed-in reader, or a 401 already written. requireAuth
// in front of every route makes the 401 unreachable; it stays so a handler
// mounted without it fails closed.
func requireUser(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
	return u, ok
}
