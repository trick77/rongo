package ask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/trick77/rongo/internal/llm"
)

// upstreamAttempt is how the fake endpoint answers ONE request. The zero value
// is a clean stream with no content at all, which is the empty completion a
// live run produced.
type upstreamAttempt struct {
	// status, when set, is an HTTP failure instead of a stream.
	status int
	// tokens are the content deltas the stream sends.
	tokens []string
	// drop aborts the connection once the tokens are out, the way a reset
	// arrives mid-answer.
	drop bool
}

// attemptsUpstream answers successive requests from the list, so a test can
// say what the first call does and what the second one does. The counter is
// atomic because the server handles each request on its own goroutine.
func attemptsUpstream(t *testing.T, attempts ...upstreamAttempt) (*llm.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		if n > len(attempts) {
			http.Error(w, `{"error":{"message":"unexpected extra request"}}`, http.StatusTeapot)
			return
		}
		a := attempts[n-1]
		if a.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(a.status)
			fmt.Fprint(w, `{"error":{"message":"no","type":"invalid_request_error"}}`)
			return
		}
		if a.drop && len(a.tokens) == 0 {
			// Nothing was sent at all: the caller sees the connection die
			// before a single header.
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := http.NewResponseController(w)
		for _, tok := range a.tokens {
			frame, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": tok}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			_ = fl.Flush()
		}
		if a.drop {
			// The tokens are on the wire and the stream stops there: no
			// finish_reason, no [DONE], no usage frame.
			panic(http.ErrAbortHandler)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		_ = fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv), &calls
}

// One of ten answer turns on a run died before its first token. The reader
// watched nothing, so nothing is lost by writing the answer again.
func TestAnswer_retriesAStreamThatBrokeBeforeItsFirstToken(t *testing.T) {
	c, calls := attemptsUpstream(t,
		upstreamAttempt{drop: true},
		upstreamAttempt{tokens: []string{"Stored in ", "store.go [1]."}},
	)
	var seen []string

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "Stored in store.go [1]." {
		t.Errorf("text = %q, want the second attempt's answer", got.Text)
	}
	if strings.Join(seen, "") != got.Text {
		t.Errorf("streamed %q, want the same text the record has", strings.Join(seen, ""))
	}
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
	if got.Kept {
		t.Error("kept = true, want false — the second attempt wrote a whole answer")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want exactly two", n)
	}
	if len(got.Citations) != 1 || got.Citations[0].Path != "backend/internal/playbackgrant/store.go" {
		t.Errorf("citations = %+v, want the second attempt's marker resolved", got.Citations)
	}
}

// Once tokens are on screen the attempt is spent: a second call would write a
// different answer over the one the reader is reading, and the record would
// stop matching the browser.
func TestAnswer_keepsTheTextOfAStreamThatDroppedAfterIt(t *testing.T) {
	c, calls := attemptsUpstream(t,
		upstreamAttempt{tokens: []string{"Stored [2] and ", "then"}, drop: true},
	)
	var seen []string

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "Stored [1] and then" {
		t.Errorf("text = %q, want the renumbered partial answer", got.Text)
	}
	if strings.Join(seen, "") != got.Text {
		t.Errorf("streamed %q, want the same text the record has", strings.Join(seen, ""))
	}
	if !got.Kept {
		t.Error("kept = false, want the turn marked as stopping before its end")
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 — a retry would replace what was read", got.Attempts)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests = %d, want one", n)
	}
	if len(got.Citations) != 1 || got.Citations[0].Path != "backend/internal/httpapi/grant.go" {
		t.Errorf("citations = %+v", got.Citations)
	}
}

// An empty completion is a clean stream that said nothing. It is the failure
// that was recorded as a failed turn most often, and it is transient.
func TestAnswer_retriesAnEmptyCompletion(t *testing.T) {
	c, calls := attemptsUpstream(t,
		upstreamAttempt{},
		upstreamAttempt{tokens: []string{"Issued in grant.go [2]."}},
	)

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "Issued in grant.go [1]." || got.Attempts != 2 {
		t.Errorf("text = %q attempts = %d, want the second attempt's answer", got.Text, got.Attempts)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want two", n)
	}
}

// Twice is where it stops: a second empty completion fails the turn with the
// same line it always did, rather than calling forever.
func TestAnswer_twoEmptyCompletionsFailTheTurn(t *testing.T) {
	c, calls := attemptsUpstream(t, upstreamAttempt{}, upstreamAttempt{})

	_, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)

	if err == nil || !strings.Contains(err.Error(), "returned no answer text") {
		t.Fatalf("err = %v, want the no-answer-text failure", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want two", n)
	}
}

// A cancelled turn is not a transient failure. Nobody is waiting for the
// answer and nobody would pay for the second call.
func TestAnswer_aCancelledTurnIsNeverRetried(t *testing.T) {
	c, calls := attemptsUpstream(t, upstreamAttempt{}, upstreamAttempt{tokens: []string{"x [1]"}})
	// A cancelled request never reaches the server, so the call count cannot
	// tell a retry apart from a refusal to retry. The log line can: it is
	// written before the second attempt and nowhere else.
	records := captureLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewAnswerer(c).Answer(ctx, "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context error", err)
	}
	for _, rec := range records() {
		if rec["msg"] == "answer retried" {
			t.Errorf("the cancelled turn was retried: %+v", rec)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}
}

// A 4xx is the host refusing the request rongo sent. The same request sent
// again is refused again, so the turn fails on the first one.
func TestAnswer_aRefusedRequestIsNeverRetried(t *testing.T) {
	c, calls := attemptsUpstream(t,
		upstreamAttempt{status: http.StatusUnauthorized},
		upstreamAttempt{tokens: []string{"x [1]"}},
	)

	_, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)

	if err == nil || !strings.Contains(err.Error(), "write the answer") {
		t.Fatalf("err = %v, want the turn to fail", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests = %d, want one — a rejected key is not transient", n)
	}
}

// A 5xx is the upstream, not the request, and it is exactly what a retry is
// for.
func TestAnswer_retriesAnUpstreamFailure(t *testing.T) {
	c, calls := attemptsUpstream(t,
		upstreamAttempt{status: http.StatusBadGateway},
		upstreamAttempt{tokens: []string{"Issued [2]."}},
	)

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Attempts != 2 || got.Text != "Issued [1]." {
		t.Errorf("attempts = %d text = %q, want the second attempt's answer", got.Attempts, got.Text)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want two", n)
	}
}

// A whole answer says so: one attempt, nothing partial.
func TestAnswer_aNormalTurnReportsOneAttempt(t *testing.T) {
	c, _, _ := streamUpstream(t, "Stored in store.go [1].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Attempts != 1 || got.Kept {
		t.Errorf("attempts = %d kept = %v, want one whole attempt", got.Attempts, got.Kept)
	}
}

// A length cut is not a broken stream: the text is kept the way it always
// was, and the turn is not marked as stopping early.
func TestAnswer_aLengthCutIsNotAPartialStream(t *testing.T) {
	c, _, _ := streamUpstreamEnding(t, "length", []string{"Stored [2] and then"})

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Kept {
		t.Error("kept = true, want a cut to stay a cut")
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
}

// The trace is part of the record, and a turn that took two calls or stopped
// early has to say so where the reader watches the timeline.
func TestWritingDetail_saysHowTheStreamWent(t *testing.T) {
	d := writingDetail(Answer{Attempts: 2}, 3)
	if d["attempts"] != 2 {
		t.Errorf("attempts = %v, want 2", d["attempts"])
	}
	if _, ok := d["partial"]; ok {
		t.Errorf("partial = %v on a whole answer, want it absent", d["partial"])
	}

	d = writingDetail(Answer{Attempts: 1, Kept: true}, 3)
	if d["attempts"] != 1 || d["partial"] != true {
		t.Errorf("detail = %+v, want one attempt reported as partial", d)
	}
}
