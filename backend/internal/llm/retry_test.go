package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// attempt is how the fake endpoint answers ONE request. The zero value is a
// clean reply with no content and nothing charged for, which is the empty
// completion a live run produced.
type attempt struct {
	// status, when set, is an HTTP failure instead of a reply.
	status int
	// retryAfter is the header sent with status.
	retryAfter string
	// content is the assistant's text: the whole reply for Complete, the
	// deltas for a stream.
	content []string
	// finish is the finish_reason the reply or the stream carries.
	finish string
	// completion is what the endpoint says it charged for.
	completion int
	// drop aborts the connection once the content is out.
	drop bool
}

// attemptsUpstream answers successive requests from the list, so a test can
// say what the first call does and what the second one does. The counter is
// atomic because the server handles each request on its own goroutine.
func attemptsUpstream(t *testing.T, attempts ...attempt) (*Client, *atomic.Int32) {
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
			if a.retryAfter != "" {
				w.Header().Set("Retry-After", a.retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(a.status)
			fmt.Fprint(w, `{"error":{"message":"no","type":"invalid_request_error"}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]any{"content": strings.Join(a.content, "")},
					"finish_reason": a.finish,
				}},
				"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": a.completion, "total_tokens": 3 + a.completion},
			})
			return
		}
		if a.drop && len(a.content) == 0 {
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := http.NewResponseController(w)
		for _, tok := range a.content {
			frame, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": tok}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			_ = fl.Flush()
		}
		if a.drop {
			panic(http.ErrAbortHandler)
		}
		if a.finish != "" {
			end, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": a.finish}},
			})
			fmt.Fprintf(w, "data: %s\n\n", end)
		}
		usage, _ := json.Marshal(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": a.completion, "total_tokens": 3 + a.completion},
		})
		fmt.Fprintf(w, "data: %s\n\n", usage)
		fmt.Fprint(w, "data: [DONE]\n\n")
		_ = fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return mustClient(t, Config{BaseURL: srv.URL, APIKey: "sk-secret"}, srv.Client()), &calls
}

// stubSleep holds the clock still and records what the wait would have been.
func stubSleep(t *testing.T) *time.Duration {
	t.Helper()
	var waited time.Duration
	prev := sleep
	sleep = func(ctx context.Context, d time.Duration) bool {
		waited = d
		return ctx.Err() == nil
	}
	t.Cleanup(func() { sleep = prev })
	return &waited
}

func complete(t *testing.T, c *Client) (string, Usage, error) {
	t.Helper()
	return c.Complete(context.Background(), []Message{{Role: "user", Content: "hallo"}})
}

func streamed(t *testing.T, c *Client) (string, Usage, error) {
	t.Helper()
	var text strings.Builder
	u, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hallo"}},
		func(tok string) { text.WriteString(tok) })
	return text.String(), u, err
}

// A 5xx is the upstream, not the request. Nothing was delivered, so the call
// is simply made again.
func TestComplete_retriesAFailureThatDeliveredNothing(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{status: http.StatusBadGateway},
		attempt{content: []string{"the answer"}, finish: "stop", completion: 7},
	)

	out, u, err := complete(t, c)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if out != "the answer" {
		t.Errorf("content = %q, want the second attempt's reply", out)
	}
	if u.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", u.Attempts)
	}
	if u.Completion != 7 {
		t.Errorf("completion = %d, want the successful attempt's figure", u.Completion)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want two", n)
	}
}

// The ordinary call says one attempt, so a step reporting the figure never has
// to guess what zero meant.
func TestComplete_aCallThatWorkedReportsOneAttempt(t *testing.T) {
	c, calls := attemptsUpstream(t, attempt{content: []string{"the answer"}, finish: "stop", completion: 7})

	_, u, err := complete(t, c)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if u.Attempts != 1 || calls.Load() != 1 {
		t.Errorf("attempts = %d over %d requests, want one of each", u.Attempts, calls.Load())
	}
}

// A stream that died before its first delta has put nothing on a screen, so
// the second call is free to write the answer from the top.
func TestStream_retriesWhenNoDeltaWasDelivered(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{drop: true},
		attempt{content: []string{"the ", "answer"}, completion: 4},
	)

	text, u, err := streamed(t, c)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if text != "the answer" || u.Attempts != 2 {
		t.Errorf("text = %q attempts = %d, want the second attempt's stream", text, u.Attempts)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want two", n)
	}
}

// Once a delta is out it is on a reader's screen. A second call would write a
// different answer over the one being read, so the error is the caller's.
func TestStream_aStreamThatAlreadyDeliveredIsNeverRetried(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{content: []string{"the ", "ans"}, drop: true},
		attempt{content: []string{"a whole other answer"}, completion: 4},
	)

	text, u, err := streamed(t, c)

	if err == nil {
		t.Fatal("err = nil, want the broken stream reported")
	}
	if text != "the ans" {
		t.Errorf("text = %q, want the deltas that did arrive", text)
	}
	if u.Attempts != 1 || calls.Load() != 1 {
		t.Errorf("attempts = %d over %d requests, want one of each", u.Attempts, calls.Load())
	}
}

// A refused request is refused again: rongo sends the same bytes twice.
func TestComplete_aRefusedRequestIsNeverRetried(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{status: http.StatusUnauthorized},
		attempt{content: []string{"the answer"}, finish: "stop", completion: 7},
	)

	if _, _, err := complete(t, c); err == nil {
		t.Fatal("err = nil, want the refusal")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests = %d, want one", n)
	}
}

// A rate limit says come back, and the host's own Retry-After says when.
func TestComplete_retriesARateLimitAfterTheWaitItAskedFor(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{status: http.StatusTooManyRequests, retryAfter: "1"},
		attempt{content: []string{"the answer"}, finish: "stop", completion: 7},
	)
	waited := stubSleep(t)

	out, u, err := complete(t, c)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if out != "the answer" || u.Attempts != 2 {
		t.Errorf("content = %q attempts = %d, want the second attempt", out, u.Attempts)
	}
	if *waited != time.Second {
		t.Errorf("waited %s, want the Retry-After the host sent", *waited)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want two", n)
	}
}

// Past the cap the host has said it will refuse, and holding the turn open for
// it is worse than failing now.
func TestComplete_aRateLimitAskingForTooLongIsNotRetried(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{status: http.StatusTooManyRequests, retryAfter: "60"},
		attempt{content: []string{"the answer"}, finish: "stop", completion: 7},
	)
	waited := stubSleep(t)

	if _, _, err := complete(t, c); err == nil {
		t.Fatal("err = nil, want the rate limit reported")
	}
	if *waited != 0 {
		t.Errorf("waited %s, want no wait at all", *waited)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests = %d, want one", n)
	}
}

// A completion budget spent thinking is the model's own doing. The second call
// spends it the same way.
func TestStream_aFinishReasonIsNeverRetried(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{finish: "length", completion: 900},
		attempt{content: []string{"the answer"}, completion: 4},
	)

	_, u, err := streamed(t, c)

	if err == nil || !strings.Contains(err.Error(), "finish_reason=length") {
		t.Fatalf("err = %v, want the finish-reason failure", err)
	}
	if u.Attempts != 1 || calls.Load() != 1 {
		t.Errorf("attempts = %d over %d requests, want one of each", u.Attempts, calls.Load())
	}
}

// A clean stop that charged for tokens is the model choosing to write nothing,
// which it would choose again. Only the transport's empty completion — nothing
// said and nothing charged — is worth a second call.
func TestStream_anEmptyCompletionIsRetriedOnlyWhenNothingWasCharged(t *testing.T) {
	c, calls := attemptsUpstream(t, attempt{}, attempt{content: []string{"the answer"}, completion: 4})
	text, u, err := streamed(t, c)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "the answer" || u.Attempts != 2 || calls.Load() != 2 {
		t.Errorf("text = %q attempts = %d over %d requests, want the second attempt", text, u.Attempts, calls.Load())
	}

	c, calls = attemptsUpstream(t,
		attempt{completion: 120},
		attempt{content: []string{"the answer"}, completion: 4},
	)
	text, u, err = streamed(t, c)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "" || u.Attempts != 1 || calls.Load() != 1 {
		t.Errorf("text = %q attempts = %d over %d requests, want the charged silence left alone",
			text, u.Attempts, calls.Load())
	}
}

// Nobody is waiting for what a second call would write.
func TestComplete_aCancelledCallIsNeverRetried(t *testing.T) {
	c, calls := attemptsUpstream(t, attempt{}, attempt{content: []string{"the answer"}, completion: 4})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hallo"}}); err == nil {
		t.Fatal("err = nil, want the context error")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}
}

// The turn's ceiling is this process's arithmetic, and it is no smaller a
// moment later.
func TestComplete_aSpentTurnBudgetIsNeverRetried(t *testing.T) {
	c, calls := attemptsUpstream(t, attempt{content: []string{"the answer"}, finish: "stop", completion: 7})
	c.turnMaxTokens = 10

	if _, _, err := c.Complete(spentMeter(50), []Message{{Role: "user", Content: "hallo"}}); err == nil {
		t.Fatal("err = nil, want the ceiling to refuse the call")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}
}

// A context error that arrives as the call's own error is still a cancelled
// call, whatever the caller's context says a moment later.
func TestRetriable_readsTheClassNotTheStatus(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		completion int
		want       bool
	}{
		{"a clean stop that said and charged nothing", nil, 0, true},
		{"a clean stop the endpoint charged for", nil, 120, false},
		{"a cancelled call", context.Canceled, 0, false},
		{"a call past its deadline", context.DeadlineExceeded, 0, false},
		{"the turn's own ceiling", ErrTurnBudget, 0, false},
		{"a spent completion budget", &FinishError{Reason: "length"}, 0, false},
		{"a transport failure", errors.New("connection reset by peer"), 0, true},
	}
	for _, tc := range cases {
		if got := retriable(tc.err, tc.completion); got != tc.want {
			t.Errorf("%s: retriable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A ceiling that runs out inside the wait ends the call there: the turn it
// belonged to is over.
func TestComplete_aTurnThatEndsDuringTheWaitIsNotCalledAgain(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{status: http.StatusTooManyRequests, retryAfter: "1"},
		attempt{content: []string{"the answer"}, finish: "stop", completion: 7},
	)
	prev := sleep
	sleep = func(context.Context, time.Duration) bool { return false }
	t.Cleanup(func() { sleep = prev })

	if _, _, err := complete(t, c); err == nil {
		t.Fatal("err = nil, want the rate limit reported")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests = %d, want one", n)
	}
}
