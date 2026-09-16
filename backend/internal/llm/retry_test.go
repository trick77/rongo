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

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/usage"
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
	// stall holds the request open before answering, which is what a call
	// running past its window looks like from here.
	stall time.Duration
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
		if a.stall > 0 {
			select {
			case <-time.After(a.stall):
			case <-r.Context().Done():
				return
			}
		}
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
		if a.drop {
			writeDeltas(w, a.content)
			panic(http.ErrAbortHandler)
		}
		writeSSE(w, a.content, a.finish, a.completion)
	}))
	t.Cleanup(srv.Close)
	return mustClient(t, Config{BaseURL: srv.URL, APIKey: "sk-secret"}, srv.Client()), &calls
}

// stubSleep holds the clock still and records what the wait would have been.
// slept is what the stub reports: false is a context that ended inside the
// wait. The jitter is pinned to zero for the same reason the clock is stopped
// — a test asserting on a wait wants the wait, not a sample of it.
func stubSleep(t *testing.T, slept bool) *time.Duration {
	t.Helper()
	var waited time.Duration
	prevSleep, prevJitter := sleep, jitter
	sleep = func(_ context.Context, d time.Duration) bool {
		waited = d
		return slept
	}
	jitter = func() time.Duration { return 0 }
	t.Cleanup(func() { sleep, jitter = prevSleep, prevJitter })
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
	waited := stubSleep(t, true)

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
	waited := stubSleep(t, true)

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

// The list is positive: a failure is retried because it is named, and an error
// nobody classified is nobody's decision to retry.
func TestRetriable_isAPositiveList(t *testing.T) {
	upstream := &llmwire.APIError{StatusCode: 502, Class: llmwire.ErrUpstream}
	shape := &llmwire.APIError{StatusCode: 200, Class: llmwire.ErrResponseShape}
	cases := []struct {
		name       string
		err        error
		completion int
		want       bool
	}{
		{"a clean stop that said and charged nothing", nil, 0, true},
		{"a clean stop the endpoint charged for", nil, 120, false},
		{"a 5xx or a transport failure", upstream, 0, true},
		{"a stream that went quiet", fmt.Errorf("llmwire: %w for 1m30s", llmwire.ErrStreamIdle), 0, true},
		{"a proxy page in place of an answer", fmt.Errorf("llmwire: %w", llmwire.ErrMalformedResponse), 0, true},
		{"a shape the endpoint chose to send", shape, 0, false},
		{"a call that ran its window out", fmt.Errorf("llmwire: %w", llmwire.ErrCallCap), 0, false},
		{"a reply that never started", fmt.Errorf("llmwire: %w", llmwire.ErrNoResponseHeaders), 0, false},
		{"a cancelled call", context.Canceled, 0, false},
		{"the turn's own ceiling", ErrTurnBudget, 0, false},
		{"a spent completion budget", &FinishError{Reason: "length"}, 0, false},
		{"an error nobody classified", errors.New("something new"), 0, false},
	}
	for _, tc := range cases {
		if got := retriable(context.Background(), tc.err, tc.completion); got != tc.want {
			t.Errorf("%s: retriable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A cancelled caller is not asked twice, whatever the call came back with.
func TestRetriable_refusesOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if retriable(ctx, nil, 0) {
		t.Error("retriable = true on a cancelled context")
	}
}

// A wait the caller has no time left for is a wait that ends in a cancelled
// call: fail now instead.
func TestRetriable_refusesARateLimitTheCallerCannotWaitOut(t *testing.T) {
	limited := &llmwire.RateLimitError{
		APIError:   &llmwire.APIError{StatusCode: 429, Class: llmwire.ErrRateLimited},
		RetryAfter: 5 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if retriable(ctx, limited, 0) {
		t.Error("retriable = true for a wait longer than the caller's ceiling")
	}

	roomy, cancel2 := context.WithTimeout(context.Background(), time.Minute)
	defer cancel2()
	if !retriable(roomy, limited, 0) {
		t.Error("retriable = false for a wait the caller has room for")
	}
}

// Every retry pauses: refiring in the same instant hits the upstream that is
// having a moment with the burst that just bounced.
func TestRetryWait_neverRefiresInTheSameInstant(t *testing.T) {
	if got := retryWait(nil); got != retryPause {
		t.Errorf("wait = %s, want the floor %s", got, retryPause)
	}
	hinted := &llmwire.RateLimitError{
		APIError:   &llmwire.APIError{StatusCode: 429, Class: llmwire.ErrRateLimited},
		RetryAfter: 2 * time.Second,
	}
	if got := retryWait(hinted); got != 2*time.Second {
		t.Errorf("wait = %s, want the host's own hint", got)
	}
	tiny := &llmwire.RateLimitError{
		APIError:   &llmwire.APIError{StatusCode: 429, Class: llmwire.ErrRateLimited},
		RetryAfter: time.Millisecond,
	}
	if got := retryWait(tiny); got != retryPause {
		t.Errorf("wait = %s, want a hint under the floor raised to it", got)
	}
}

// A ceiling that runs out inside the wait ends the call there: the turn it
// belonged to is over.
func TestComplete_aTurnThatEndsDuringTheWaitIsNotCalledAgain(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{status: http.StatusTooManyRequests, retryAfter: "1"},
		attempt{content: []string{"the answer"}, finish: "stop", completion: 7},
	)
	stubSleep(t, false)

	if _, _, err := complete(t, c); err == nil {
		t.Fatal("err = nil, want the rate limit reported")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests = %d, want one", n)
	}
}

// A per-attempt window is what lets a stalled first call leave the retry any
// time at all: without it one call that hangs spends the whole ceiling.
func TestComplete_aStalledAttemptLeavesTheSecondOneAWindow(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{stall: time.Second, content: []string{"too late"}, finish: "stop", completion: 7},
		attempt{content: []string{"the answer"}, finish: "stop", completion: 7},
	)
	stubSleep(t, true)

	out, u, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hallo"}},
		WithAttemptTimeout(30*time.Millisecond))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if out != "the answer" || u.Attempts != 2 {
		t.Errorf("content = %q attempts = %d, want the second attempt's reply", out, u.Attempts)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("requests = %d, want two", n)
	}
}

// Without the option the caller's ceiling is the only bound, which is what the
// answer call wants: one attempt worth making, with a reader watching it.
func TestComplete_withoutTheOptionOneCallMayTakeTheWholeCeiling(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{stall: 30 * time.Millisecond, content: []string{"slow but fine"}, finish: "stop", completion: 7},
		attempt{content: []string{"never asked for"}, finish: "stop", completion: 7},
	)

	out, u, err := complete(t, c)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if out != "slow but fine" || u.Attempts != 1 || calls.Load() != 1 {
		t.Errorf("content = %q attempts = %d over %d requests, want the slow call left alone",
			out, u.Attempts, calls.Load())
	}
}

// One doubled call is a hiccup. Doubling every call of a turn after the first
// retry already failed is an outage paid for twice, on a reader's clock.
func TestRetry_onceARetryHasFailedTheTurnMakesNoMore(t *testing.T) {
	c, calls := attemptsUpstream(t,
		attempt{status: http.StatusBadGateway},
		attempt{status: http.StatusBadGateway},
		attempt{status: http.StatusBadGateway},
	)
	stubSleep(t, true)
	ctx := usage.WithMeter(context.Background(), usage.New())

	if _, _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hallo"}}); err == nil {
		t.Fatal("err = nil, want the first call to fail twice")
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("requests = %d after the first call, want two", n)
	}

	if _, _, err := c.Complete(ctx, []Message{{Role: "user", Content: "nochmal"}}); err == nil {
		t.Fatal("err = nil, want the second call to fail")
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("requests = %d in total, want three — the second call goes out once", n)
	}
}

// The first attempt was paid for. A turn whose ceiling went in that payment
// does not get to spend past it on a second one.
func TestRetry_aCeilingSpentOnTheFirstAttemptStopsTheSecond(t *testing.T) {
	c, calls := attemptsUpstream(t, attempt{}, attempt{content: []string{"the answer"}, finish: "stop", completion: 7})
	c.turnMaxTokens = 2
	stubSleep(t, true)
	ctx := usage.WithMeter(context.Background(), usage.New())

	// The empty completion is retriable, and the three prompt tokens the
	// endpoint reported for it are over the ceiling.
	if _, u, err := c.Complete(ctx, []Message{{Role: "user", Content: "hallo"}}); err != nil || u.Attempts != 1 {
		t.Fatalf("Complete: %v, attempts = %d, want one attempt", err, u.Attempts)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("requests = %d, want one", n)
	}
}
