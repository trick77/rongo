package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noFlush is a ResponseWriter that cannot flush: sse.Open refuses it.
type noFlush struct{ http.ResponseWriter }

// A stream that never opened swallows the turn's events rather than
// panicking, the same as one whose reader left.
func TestStream_oneThatFailedToOpenSwallowsSends(t *testing.T) {
	rec := httptest.NewRecorder()
	st := openStream(noFlush{rec})

	st.send("token", map[string]any{"text": "x"})
	st.close()

	if rec.Body.Len() != 0 {
		t.Errorf("wrote %q to a stream that never opened", rec.Body.String())
	}
}

// Events go out framed, and nothing is written once the stream is closed:
// the title goroutine may still be sending when the handler returns.
func TestStream_framesEventsAndRefusesAfterClose(t *testing.T) {
	rec := httptest.NewRecorder()
	st := openStream(rec)

	st.send("token", map[string]any{"text": "x"})
	st.close()
	st.send("title", map[string]any{"title": "late"})

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: token\ndata: {\"text\":\"x\"}\n\n") {
		t.Errorf("body = %q, want the token event", body)
	}
	if strings.Contains(body, "late") {
		t.Errorf("body = %q: a send after close was written", body)
	}
}
