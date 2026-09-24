package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// sseStream is the answer's wire: one server-sent-events response, written
// from the handler and — for the title — from a goroutine outside it. The
// writer is taken under a lock and the stream is shut to further writes the
// moment the handler returns: a write to a ResponseWriter whose handler has
// finished is not merely ignored, it races the server's own cleanup.
type sseStream struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	mu     sync.Mutex
	closed bool
}

// openStream writes the headers and the status. Once anything is written the
// status code is fixed, so every failure after this point is an SSE error
// event, not a 500 the browser could still act on.
func openStream(w http.ResponseWriter) *sseStream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &sseStream{w: w, rc: http.NewResponseController(w)}
}

// send writes one event. A payload that cannot be marshalled is dropped;
// a stream already closed swallows the write.
func (s *sseStream) send(event string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, body)
	_ = s.rc.Flush()
}

// close shuts the stream to further writes. Deferred by the handler, so it
// runs when the handler returns — after the deferred title wait, which is
// registered later and so runs first.
func (s *sseStream) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}
