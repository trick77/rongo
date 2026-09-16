package ask

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/trick77/rongo/internal/llm"
)

// failThenStream refuses the first request with a 5xx and streams the tokens
// to the second, which is what a transient upstream failure looks like from
// here. The counter is atomic because each request gets its own goroutine.
func failThenStream(t *testing.T, tokens ...string) (*llm.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, `{"error":{"message":"upstream is having a moment"}}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, tokens, "")
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv), &calls
}

// breakAfter streams the tokens and then drops the connection, with no finish
// reason, no usage frame and no [DONE].
func breakAfter(t *testing.T, tokens ...string) (*llm.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeDeltas(w, tokens)
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv), &calls
}

// The client retries the call; what the trace has to say is how many attempts
// the reader waited through, which is why the figure travels on the usage.
func TestAnswer_aRetriedAnswerReportsTwoAttemptsInItsDetail(t *testing.T) {
	c, calls := failThenStream(t, "Stored in ", "store.go [1].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "Stored in store.go [1]." {
		t.Errorf("text = %q, want the second attempt's answer", got.Text)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want two", n)
	}
	if d := writingDetail(got, 2); d["attempts"] != 2 {
		t.Errorf("attempts = %v, want the trace to say the turn took two calls", d["attempts"])
	}
}

// A stream that broke after text fails the turn, and fails it on that one
// call. The fragment stops mid-sentence, possibly inside a fence; nothing
// downstream knows it is a fragment, and the reader still has the retry
// button.
func TestAnswer_aStreamThatBrokeAfterTextFailsTheTurn(t *testing.T) {
	c, calls := breakAfter(t, "Stored [2] and ", "then")

	_, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)

	if err == nil {
		t.Fatal("err = nil, want the turn to fail rather than store a fragment")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests = %d, want one — a retry would replace what was read", n)
	}
}

// A turn that went through in one call says nothing about attempts: one is
// what every turn takes, and a line saying so on all of them is noise.
func TestAnswer_anOrdinaryTurnSaysNothingAboutAttempts(t *testing.T) {
	c, _, _ := streamUpstream(t, "Stored in store.go [1].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if d := writingDetail(got, 2); d["attempts"] != nil {
		t.Errorf("attempts = %v, want it absent on a turn that took one call", d["attempts"])
	}
}
