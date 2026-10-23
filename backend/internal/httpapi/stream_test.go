package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noFlush is a ResponseWriter that cannot flush: sse.Open refuses it.
type noFlush struct{ http.ResponseWriter }

// A writer that cannot flush is refused before anything is sent: a bare 500,
// and false so the handler returns before the turn starts.
func TestStream_aWriterThatCannotFlushIsA500(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ask", nil)

	st, ok := openStream(noFlush{rec}, req)

	if ok || st != nil {
		t.Fatalf("openStream = %v, %v; want nil, false", st, ok)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want the JSON error, not an event stream", got)
	}
}

// Events go out framed, and nothing is written once the stream is closed:
// the title goroutine may still be sending when the handler returns.
func TestStream_framesEventsAndRefusesAfterClose(t *testing.T) {
	rec := httptest.NewRecorder()
	st, _ := openStream(rec, httptest.NewRequest(http.MethodPost, "/api/ask", nil))

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

// A stream that cannot open is refused before the question is recorded and
// before the paid turn starts. Behind the middleware the writer claims to
// flush, so the failure is the first flush, after the 200: no events, no
// turn, and no row for a turn that never ran.
func TestAsk_aStreamThatCannotOpenStartsNoTurn(t *testing.T) {
	// Given
	asker := &fakeAsker{tokens: []string{"never"}}
	deps, st := askDeps(t, asker)

	// When
	req := httptest.NewRequest(http.MethodPost, "/api/ask",
		strings.NewReader(`{"question":"How does shipping work?","audience":"ba"}`))
	rec := httptest.NewRecorder()
	NewServer(deps).ServeHTTP(noFlush{rec}, req)

	// Then
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want nothing on a stream that never opened", rec.Body.String())
	}
	if asker.gotAud != "" {
		t.Error("the turn ran on a stream that never opened")
	}
	list, err := st.List(context.Background(), testSubject)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, th := range list {
		msgs, err := st.Messages(context.Background(), testSubject, th.ID)
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(msgs) != 0 {
			t.Errorf("thread %d holds %d messages, want none recorded", th.ID, len(msgs))
		}
	}
}

// The same for a re-explain: no new turn is recorded.
func TestReexplain_aStreamThatCannotOpenStartsNoTurn(t *testing.T) {
	// Given
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining())
	msgID := seedAnsweredMessageWithSources(t, store, db)
	orig, _, err := store.Message(context.Background(), testSubject, msgID)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}

	// When
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/messages/%d/reexplain", msgID),
		strings.NewReader(`{"audience":"dev"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(noFlush{rec}, req)

	// Then
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want nothing on a stream that never opened", rec.Body.String())
	}
	msgs, err := store.Messages(context.Background(), testSubject, orig.ThreadID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("messages = %d, want only the original turn", len(msgs))
	}
}
