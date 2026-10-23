package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/trick77/ship/kit/sse"
)

// sseStream is the answer's wire: kit's sse.Writer, written from the handler
// and — for the title — from a goroutine outside it. The Writer serializes
// those writes and refuses every one after close: a write to a ResponseWriter
// whose handler has finished is not merely ignored, it races the server's own
// cleanup.
type sseStream struct {
	w *sse.Writer
}

// openStream writes the headers and the status. Once anything is written the
// status code is fixed, so every failure after this point is an SSE error
// event, not a 500 the browser could still act on. A stream that fails to
// open reports false and the caller returns before the turn is recorded or
// paid for: there is nobody to send its events to. A writer that cannot
// flush is refused with a 500, nothing being written yet; behind kit's
// middleware that shows as a failed first flush instead, after the 200.
func openStream(w http.ResponseWriter, r *http.Request) (*sseStream, bool) {
	sw, err := sse.Open(w)
	if errors.Is(err, sse.ErrNoFlush) {
		serverError(w, r, "open event stream failed", err)
		return nil, false
	}
	if err != nil {
		slog.WarnContext(r.Context(), "open event stream failed", "err", err)
		return nil, false
	}
	return &sseStream{w: sw}, true
}

// send writes one event. A payload that cannot be marshalled is dropped; a
// failed write closes the stream, and a closed one swallows the write.
func (s *sseStream) send(event string, payload any) {
	_ = s.w.SendJSON(event, payload)
}

// close shuts the stream to further writes. Deferred by the handler, so it
// runs when the handler returns — after the deferred title wait, which is
// registered later and so runs first.
func (s *sseStream) close() {
	s.w.Close()
}
