package httpapi

import (
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
	w *sse.Writer // nil when the stream never opened
}

// openStream writes the headers and the status. Once anything is written the
// status code is fixed, so every failure after this point is an SSE error
// event, not a 500 the browser could still act on. A stream that fails to
// open swallows every send, as one whose reader left does: the turn still
// runs and is recorded.
func openStream(w http.ResponseWriter) *sseStream {
	sw, err := sse.Open(w)
	if err != nil {
		slog.Warn("open event stream failed", "err", err)
	}
	return &sseStream{w: sw}
}

// send writes one event. A payload that cannot be marshalled is dropped; a
// failed write closes the stream, and a closed one swallows the write.
func (s *sseStream) send(event string, payload any) {
	if s.w != nil {
		_ = s.w.SendJSON(event, payload)
	}
}

// close shuts the stream to further writes. Deferred by the handler, so it
// runs when the handler returns — after the deferred title wait, which is
// registered later and so runs first.
func (s *sseStream) close() {
	if s.w != nil {
		s.w.Close()
	}
}
