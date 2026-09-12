package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/trick77/llmwire"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/usage"
)

func TestComplete_recordsTheCallIntoTheContextsMeterUnderItsStep(t *testing.T) {
	// Given
	c, _ := fakeUpstream(t, "ok")
	m := usage.New()
	ctx := usage.WithMeter(context.Background(), m)

	// When
	if _, _, err := c.Complete(ctx, []Message{{Role: "user", Content: "x"}}, ShortGate(), WithStep("understand")); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Then: the deployment the call went to and the upstream's numbers.
	calls := m.Calls()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(calls))
	}
	want := usage.Call{Step: "understand", Model: ShortGateDeployment, Prompt: 11, Completion: 7}
	// The duration is measured here rather than reported by the endpoint, so
	// it is checked on its own and then cleared: what this test is about is
	// the step, the deployment and the upstream's own numbers.
	got := calls[0]
	if got.Ms == nil {
		t.Error("the call must be timed")
	}
	got.Ms = nil
	if got != want {
		t.Errorf("call = %+v, want %+v", got, want)
	}
}

func TestStream_recordsTheTrailingUsageFrameIntoTheMeter(t *testing.T) {
	// Given
	c := streamingUpstream(t, []string{"a", "b"}, "", 4)
	m := usage.New()
	ctx := usage.WithMeter(context.Background(), m)

	// When
	if _, err := c.Stream(ctx, []Message{{Role: "user", Content: "x"}}, func(string) {}, WithStep("answer")); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Then
	calls := m.Calls()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(calls))
	}
	want := usage.Call{Step: "answer", Model: ProDeployment, Prompt: 3, Completion: 4}
	got := calls[0]
	if got.Ms == nil {
		t.Error("the call must be timed")
	}
	got.Ms = nil
	if got != want {
		t.Errorf("call = %+v, want %+v", got, want)
	}
}

func TestStream_aStreamWithoutAUsageFrameRecordsNothingNotZeros(t *testing.T) {
	// Given: an upstream that streams tokens and ends without ever reporting
	// usage, the shape of a dropped connection or an endpoint that ignores
	// include_usage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		frame, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{"content": "half"}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", frame)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	m := usage.New()

	// When
	if _, err := c.Stream(usage.WithMeter(context.Background(), m), []Message{{Role: "user", Content: "x"}}, func(string) {}, WithStep("answer")); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Then: no row. A zero row would say the call was free.
	if calls := m.Calls(); len(calls) != 0 {
		t.Errorf("recorded %+v, want nothing for an unknown usage", calls)
	}
}

func TestComplete_withoutAMeterRecordsNothingAndStillAnswers(t *testing.T) {
	c, _ := fakeUpstream(t, "ok")
	out, _ := ask(t, c, WithStep("route"))
	if out != "ok" {
		t.Errorf("out = %q", out)
	}
}

func TestComplete_recordsTheCachedAndReasoningSharesAndHowLongItTook(t *testing.T) {
	// Given an endpoint that reports both details objects, as MiMo does
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3267,"completion_tokens":64,"total_tokens":3331,
			"prompt_tokens_details":{"cached_tokens":3264},
			"completion_tokens_details":{"reasoning_tokens":40}}}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Config{BaseURL: srv.URL, APIKey: "s3cret"}, srv.Client())
	m := usage.New()
	ctx := usage.WithMeter(context.Background(), m)

	// When
	if _, _, err := c.Complete(ctx, []Message{{Role: "user", Content: "x"}}, WithStep("answer")); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Then the shares the decoder used to discard are on the record
	got := m.Calls()[0]
	if got.Cached == nil || *got.Cached != 3264 {
		t.Fatalf("cached = %v, want 3264", got.Cached)
	}
	if got.Reasoning == nil || *got.Reasoning != 40 {
		t.Fatalf("reasoning = %v, want 40", got.Reasoning)
	}
	if got.Ms == nil {
		t.Fatal("ms = absent, want the call timed")
	}
	// And the cached share is a subset: the tokens themselves are unchanged.
	if got.Prompt != 3267 {
		t.Fatalf("prompt = %d, want 3267 whatever was cached", got.Prompt)
	}
}

func TestComplete_anEndpointThatSendsNoDetailsLeavesThemAbsentNotZero(t *testing.T) {
	// Given the fake upstream, which reports the three plain figures only
	c, _ := fakeUpstream(t, "ok")
	m := usage.New()

	// When
	if _, _, err := c.Complete(usage.WithMeter(context.Background(), m), []Message{{Role: "user", Content: "x"}}, WithStep("route")); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Then nothing is claimed about a cache nobody mentioned
	got := m.Calls()[0]
	if got.Cached != nil || got.Reasoning != nil {
		t.Fatalf("cached=%v reasoning=%v, want both absent", got.Cached, got.Reasoning)
	}
	if got.Ms == nil {
		t.Fatal("the duration is measured here, not reported by the endpoint; it must be present")
	}
}

func TestStream_recordsTheDetailsFromTheTrailingUsageFrame(t *testing.T) {
	// Given a stream whose trailing usage frame carries the details objects
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,`+
			`"prompt_tokens_details":{"cached_tokens":64},"completion_tokens_details":{"reasoning_tokens":0}}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Config{BaseURL: srv.URL, APIKey: "sk-secret"}, srv.Client())
	m := usage.New()

	// When
	if _, err := c.Stream(usage.WithMeter(context.Background(), m), []Message{{Role: "user", Content: "x"}}, nil, WithStep("answer")); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Then
	got := m.Calls()[0]
	if got.Cached == nil || *got.Cached != 64 {
		t.Fatalf("cached = %v, want 64", got.Cached)
	}
	// A zero the endpoint stated is a measurement, and is kept as zero.
	if got.Reasoning == nil || *got.Reasoning != 0 {
		t.Fatalf("reasoning = %v, want a stated zero", got.Reasoning)
	}
}

// spentMeter is a turn that has already paid for n tokens.
func spentMeter(n int) context.Context {
	m := usage.New()
	m.Record(usage.Call{Step: "understand", Model: ShortGateDeployment, Prompt: n, Completion: 0})
	return usage.WithMeter(context.Background(), m)
}

func TestComplete_refusesTheNextCallOnceTheTurnHasSpentItsCeiling(t *testing.T) {
	// Given a client with a ceiling and a turn that has reached it
	c, got := fakeUpstream(t, "ok")
	c.turnMaxTokens = 100
	ctx := spentMeter(100)

	// When
	_, _, err := c.Complete(ctx, []Message{{Role: "user", Content: "x"}}, WithStep("answer"))

	// Then: refused before any request, and the reason is testable
	if !errors.Is(err, ErrTurnBudget) {
		t.Fatalf("err = %v, want ErrTurnBudget", err)
	}
	if !strings.Contains(err.Error(), "100 of 100") {
		t.Fatalf("err = %v, want the figures", err)
	}
	if got.Model != "" {
		t.Fatal("the upstream must not see a request the ceiling refused")
	}
}

func TestStream_refusesTheNextCallOnceTheTurnHasSpentItsCeiling(t *testing.T) {
	// Given
	c := streamingUpstream(t, []string{"never"}, "stop", 1)
	c.turnMaxTokens = 100
	ctx := spentMeter(250)
	var tokens []string

	// When
	_, err := c.Stream(ctx, []Message{{Role: "user", Content: "x"}}, func(s string) { tokens = append(tokens, s) }, WithStep("answer"))

	// Then
	if !errors.Is(err, ErrTurnBudget) {
		t.Fatalf("err = %v, want ErrTurnBudget", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("streamed %v after the ceiling refused the call", tokens)
	}
}

func TestComplete_underTheCeilingTheCallGoesThrough(t *testing.T) {
	// Given a turn one token short of the ceiling
	c, got := fakeUpstream(t, "ok")
	c.turnMaxTokens = 100
	ctx := spentMeter(99)

	// When
	out, _, err := c.Complete(ctx, []Message{{Role: "user", Content: "x"}}, WithStep("answer"))

	// Then: the call runs to its own cap; the tripwire stops the one after it
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v, want ok", out, err)
	}
	if got.Model == "" {
		t.Fatal("the upstream must have seen the request")
	}
	if _, _, err := c.Complete(ctx, []Message{{Role: "user", Content: "x"}}, WithStep("followups")); !errors.Is(err, ErrTurnBudget) {
		t.Fatalf("the call after the ceiling was crossed must be refused, got %v", err)
	}
}

func TestComplete_noCeilingOrNoMeterIsNeverRefused(t *testing.T) {
	// Given: ceiling off, a turn far past any figure
	c, _ := fakeUpstream(t, "ok")
	c.turnMaxTokens = 0
	if _, _, err := c.Complete(spentMeter(1_000_000), []Message{{Role: "user", Content: "x"}}, WithStep("answer")); err != nil {
		t.Fatalf("with the ceiling off: %v", err)
	}

	// Given: ceiling on, a context without a meter (the indexer's)
	c.turnMaxTokens = 1
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, WithStep("answer")); err != nil {
		t.Fatalf("without a meter: %v", err)
	}
}

// captured is one request body as the fake upstream saw it.
type captured struct {
	Model               string    `json:"model"`
	Messages            []Message `json:"messages"`
	Stream              bool      `json:"stream"`
	MaxCompletionTokens int       `json:"max_completion_tokens"`
	Thinking            *struct {
		Type string `json:"type"`
	} `json:"thinking"`
	Temperature *float64 `json:"temperature"`
}

// fakeUpstream answers one chat completion and records what it was asked.
func fakeUpstream(t *testing.T, reply string) (*Client, *captured) {
	t.Helper()
	return fakeUpstreamEnding(t, reply, "stop")
}

// fakeUpstreamEnding is fakeUpstream with the finish_reason the reply carries.
func fakeUpstreamEnding(t *testing.T, reply string, finishReason string) (*Client, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, got); err != nil {
			t.Errorf("upstream got unparseable body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}, "finish_reason": finishReason}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		})
	}))
	t.Cleanup(srv.Close)
	return NewClient(Config{BaseURL: srv.URL, APIKey: "s3cret"}, srv.Client()), got
}

func ask(t *testing.T, c *Client, opts ...Option) (string, Usage) {
	t.Helper()
	out, usage, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hallo"}}, opts...)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return out, usage
}

func TestComplete_usesProAndReturnsUsage(t *testing.T) {
	// Given
	c, got := fakeUpstream(t, "the answer")

	// When
	out, usage := ask(t, c)

	// Then
	if out != "the answer" {
		t.Errorf("content = %q", out)
	}
	if got.Model != ProDeployment {
		t.Errorf("model = %q, want the Pro deployment", got.Model)
	}
	if usage.Prompt != 11 || usage.Completion != 7 || usage.Total != 18 {
		t.Errorf("usage = %+v, want the upstream's numbers", usage)
	}
}

func TestShortGate_changesTheDeploymentButNotThinking(t *testing.T) {
	// ShortGate picks the deployment that queues less. It is NOT "the model
	// that cannot think": both deployments reason, and suppressing thought is a
	// separate switch. Coupling them here is the mistake AGENTS.md names.
	c, got := fakeUpstream(t, "x")

	ask(t, c, ShortGate())

	if got.Model != ShortGateDeployment {
		t.Errorf("model = %q, want the non-Pro deployment", got.Model)
	}
	if got.Thinking != nil {
		t.Errorf("thinking = %+v, want it untouched — ShortGate must not disable reasoning", got.Thinking)
	}
}

func TestWithoutThinking_suppressesThoughtButKeepsTheDeployment(t *testing.T) {
	// The other half of the same separation.
	c, got := fakeUpstream(t, "x")

	ask(t, c, WithoutThinking())

	if got.Thinking == nil || got.Thinking.Type != "disabled" {
		t.Errorf("thinking = %+v, want disabled", got.Thinking)
	}
	if got.Model != ProDeployment {
		t.Errorf("model = %q, want the Pro deployment — WithoutThinking must not reroute", got.Model)
	}
}

func TestComplete_everyCallCarriesACompletionCap(t *testing.T) {
	// An uncapped call can run until the context dies, and the bill is the
	// first place it shows.
	c, got := fakeUpstream(t, "x")

	ask(t, c)

	if got.MaxCompletionTokens <= 0 {
		t.Errorf("max_completion_tokens = %d, want a default cap", got.MaxCompletionTokens)
	}

	c2, got2 := fakeUpstream(t, "x")
	ask(t, c2, WithMaxTokens(64))
	if got2.MaxCompletionTokens != 64 {
		t.Errorf("max_completion_tokens = %d, want the explicit 64", got2.MaxCompletionTokens)
	}
}

// TestWithTemperature_isSentAndIsOtherwiseTheEndpointsDefault pins the third
// switch, separate from the other two the way ShortGate and WithoutThinking
// are separate from each other.
//
// It exists because of a measurement: the routing arm was run twice over
// identical frozen inputs and the same corpus, and the judge decided three
// questions differently — 41/61 against 44/61. Nothing had changed but the
// sampling, because no request carried a temperature at all and the endpoint's
// default applied. A gate whose whole output is one word out of two must not
// re-roll it.
func TestWithTemperature_isSentAndIsOtherwiseTheEndpointsDefault(t *testing.T) {
	// Given a call that names no temperature
	c, got := fakeUpstream(t, "x")

	ask(t, c)

	// Then nothing is sent, and the endpoint keeps deciding — the answer call
	// is written for a reader, and pinning it here would change what everyone
	// reads to fix a routing measurement.
	if got.Temperature != nil {
		t.Errorf("temperature = %v, want it absent unless a call asks for one", *got.Temperature)
	}

	// When a call does name one
	c2, got2 := fakeUpstream(t, "x")
	ask(t, c2, WithTemperature(0))

	if got2.Temperature == nil {
		t.Fatal("temperature = absent, want the explicit 0 to reach the endpoint")
	}
	if *got2.Temperature != 0 {
		t.Errorf("temperature = %v, want 0", *got2.Temperature)
	}
	// And it reroutes nothing and suppresses nothing.
	if got2.Model != ProDeployment {
		t.Errorf("model = %q, want the Pro deployment — WithTemperature must not reroute", got2.Model)
	}
	if got2.Thinking != nil {
		t.Errorf("thinking = %+v, want it untouched", got2.Thinking)
	}
}

func TestComplete_theApiKeyNeverReachesAnError(t *testing.T) {
	// A 500 body is quoted into the error, and an upstream that echoes the
	// Authorization header would leak the key into every log line.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream refused: "+r.Header.Get("Authorization"), http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Config{BaseURL: srv.URL, APIKey: "sk-secret-value"}, srv.Client())

	_, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})

	if err == nil {
		t.Fatal("want an error for a 500")
	}
	if strings.Contains(err.Error(), "sk-secret-value") {
		t.Fatalf("the API key leaked into the error: %v", err)
	}
}

// streamingUpstream sends the reply one token per SSE frame, flushing each, so a
// client that only works when the whole body arrives at once fails here. A fake
// that answers in a single chunk would let a buffering implementation pass.
//
// The stream then ends the way the upstream says it did: finishReason, when
// set, goes out on an empty delta; then the usage frame with the given
// completion count (prompt 3, total 3+completion); then [DONE].
func streamingUpstream(t *testing.T, tokens []string, finishReason string, completion int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := http.NewResponseController(w)
		for _, tok := range tokens {
			frame, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": tok}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			_ = fl.Flush()
			time.Sleep(2 * time.Millisecond)
		}
		if finishReason != "" {
			end, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finishReason}},
			})
			fmt.Fprintf(w, "data: %s\n\n", end)
		}
		usage, _ := json.Marshal(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": completion, "total_tokens": 3 + completion},
		})
		fmt.Fprintf(w, "data: %s\n\n", usage)
		fmt.Fprint(w, "data: [DONE]\n\n")
		_ = fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return NewClient(Config{BaseURL: srv.URL, APIKey: "sk-secret"}, srv.Client())
}

// rawStreamUpstream answers 200 with the one frame given and closes: the shape
// of an OpenAI-style server that fails after it has already sent the status.
func rawStreamUpstream(t *testing.T, frame string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: %s\n\n", frame)
	}))
	t.Cleanup(srv.Close)
	return NewClient(Config{BaseURL: srv.URL, APIKey: "sk-secret"}, srv.Client())
}

func TestStream_deliversTokensOneByOne(t *testing.T) {
	// Given
	c := streamingUpstream(t, []string{"The ", "shipping ", "runs"}, "", 4)
	var seen []string

	// When
	usage, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}},
		func(tok string) { seen = append(seen, tok) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Then: three separate callbacks, in order. One callback carrying the whole
	// text would mean the answer only appears when it is finished.
	if len(seen) != 3 {
		t.Fatalf("callbacks = %d (%q), want one per token", len(seen), seen)
	}
	if strings.Join(seen, "") != "The shipping runs" {
		t.Errorf("assembled = %q", strings.Join(seen, ""))
	}
	if usage.Total != 7 {
		t.Errorf("usage = %+v, want the trailing usage frame", usage)
	}
}

func TestStream_anUpstreamThatStallsIsAbandoned(t *testing.T) {
	// An upstream that sends one delta and then goes quiet used to hold the
	// reader on a half-written answer until the coarse HTTP timeout — minutes
	// of a cursor that looks like it is still thinking. The watchdog was
	// configured and documented but never armed; this test is what says it is.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := http.NewResponseController(w)
		frame, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{"content": "The "}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", frame)
		_ = fl.Flush()
		// ...and then nothing, for longer than the watchdog window.
		time.Sleep(2 * time.Second)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Config{BaseURL: srv.URL, IdleTimeout: 150 * time.Millisecond}, srv.Client())

	var seen []string
	start := time.Now()
	_, _ = c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}},
		func(tok string) { seen = append(seen, tok) })
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Errorf("Stream took %v, want it abandoned near the idle window", elapsed)
	}
	if len(seen) != 1 || seen[0] != "The " {
		t.Errorf("tokens = %q, want the one delta that did arrive", seen)
	}
}

func TestStream_requestsUsageInTheStream(t *testing.T) {
	c, got := fakeUpstream(t, "x")
	_, _ = c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}}, func(string) {})

	if !got.Stream {
		t.Error("stream = false on a streaming call")
	}
}

// headerCapture answers one call — a JSON completion or an SSE stream — and
// hands back the headers the upstream saw.
func headerCapture(t *testing.T, stream bool, emulate bool) (*Client, *http.Header) {
	t.Helper()
	got := &http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.Header.Clone()
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}},
		})
	}))
	t.Cleanup(srv.Close)
	return NewClient(Config{BaseURL: srv.URL, APIKey: "k", EmulateOpenCode: emulate}, srv.Client()), got
}

// TestEmulateOpenCode_presentsAsTheOpencodeClient covers what the flag buys
// on both entry points: opencode's own client string, and the session header
// pair that pins a burst of calls to one upstream node. Both values are
// llmwire's; what is pinned here is that rongo's flag reaches the wire, and
// that a process sends one id for all of its calls.
func TestEmulateOpenCode_presentsAsTheOpencodeClient(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "Complete"
		if stream {
			name = "Stream"
		}
		t.Run(name, func(t *testing.T) {
			// Given
			c, got := headerCapture(t, stream, true)

			// When
			if err := callOnce(c, stream); err != nil {
				t.Fatalf("call: %v", err)
			}

			// Then
			if ua := got.Get("User-Agent"); ua != llmwire.OpenCodeUserAgent {
				t.Errorf("User-Agent = %q, want %q", ua, llmwire.OpenCodeUserAgent)
			}
			id := got.Get("X-Session-Id")
			if !strings.HasPrefix(id, "ses_") {
				t.Fatalf("X-Session-Id = %q, want an opencode-shaped session id", id)
			}
			if affinity := got.Get("X-Session-Affinity"); affinity != id {
				t.Errorf("X-Session-Affinity = %q, want the same value as X-Session-Id %q", affinity, id)
			}

			// And a second call carries the same id: one process, one session.
			if err := callOnce(c, stream); err != nil {
				t.Fatalf("second call: %v", err)
			}
			if again := got.Get("X-Session-Id"); again != id {
				t.Errorf("session id changed between calls: %q then %q", id, again)
			}
		})
	}
}

// TestEmulateOpenCode_offSendsNeitherTheStringNorTheHeaders: an endpoint that
// does not care gets llmwire's own client string and no session pair. Never
// Go's default either — that names the HTTP library and says nothing about
// the protocol being spoken.
func TestEmulateOpenCode_offSendsNeitherTheStringNorTheHeaders(t *testing.T) {
	// Given
	c, got := headerCapture(t, false, false)

	// When
	if err := callOnce(c, false); err != nil {
		t.Fatalf("call: %v", err)
	}

	// Then
	ua := got.Get("User-Agent")
	if ua == "" || strings.HasPrefix(ua, "Go-http-client") || ua == llmwire.OpenCodeUserAgent {
		t.Errorf("User-Agent = %q, want llmwire's own string", ua)
	}
	if id := got.Get("X-Session-Id"); id != "" {
		t.Errorf("X-Session-Id = %q, want none without emulation", id)
	}
}

// callOnce makes one trivial request over whichever entry point is under test.
func callOnce(c *Client, stream bool) error {
	msgs := []Message{{Role: "user", Content: "x"}}
	if stream {
		_, err := c.Stream(context.Background(), msgs, func(string) {})
		return err
	}
	_, _, err := c.Complete(context.Background(), msgs)
	return err
}

func TestStream_aStopFinishIsSuccess(t *testing.T) {
	c := streamingUpstream(t, []string{"ok"}, "stop", 4096)

	usage, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}}, func(string) {})

	if err != nil {
		t.Fatalf("Stream: %v, want a normal stop to succeed", err)
	}
	if usage.Completion != 4096 {
		t.Errorf("usage = %+v, want the trailing usage frame", usage)
	}
}

func TestStream_aLengthFinishWithNoContentIsAnErrorNamingTheBudget(t *testing.T) {
	// A reasoning model that spends the whole completion budget thinking ends
	// the stream with finish_reason=length and not one content delta. Treating
	// that as success stored an empty answer as a finished turn in production.
	c := streamingUpstream(t, nil, "length", 4096)

	usage, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}}, func(string) {})

	if err == nil {
		t.Fatal("Stream: nil error on finish_reason=length with no content")
	}
	if !strings.Contains(err.Error(), "length") {
		t.Errorf("err = %v, want the finish reason named", err)
	}
	if !strings.Contains(err.Error(), "4096") {
		t.Errorf("err = %v, want the completion token count, so the cap can be calibrated", err)
	}
	if usage.Completion != 4096 {
		t.Errorf("usage = %+v, want it read to the end even on failure", usage)
	}
}

func TestStream_aLengthFinishAfterContentIsStillAnError(t *testing.T) {
	// Truncated mid-answer is a failure too: the caller decides what to do with
	// the partial text, but it must know.
	c := streamingUpstream(t, []string{"The ", "shipping"}, "length", 4096)
	var seen []string

	_, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}},
		func(tok string) { seen = append(seen, tok) })

	if err == nil {
		t.Fatal("Stream: nil error on a truncated answer")
	}
	var cut *FinishError
	if !errors.As(err, &cut) || cut.Reason != "length" || cut.Completion != 4096 {
		t.Errorf("err = %v, want a FinishError the caller can pick apart", err)
	}
	if len(seen) != 2 {
		t.Errorf("tokens = %q, want every delta that arrived delivered before the error", seen)
	}
}

func TestStream_anErrorFrameIsAnErrorAndNeverQuotesTheKey(t *testing.T) {
	c := rawStreamUpstream(t, `{"error":{"message":"context length exceeded for Bearer sk-secret","type":"invalid_request_error"}}`)

	_, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}}, func(string) {})

	if err == nil {
		t.Fatal("Stream: nil error on an in-stream error frame")
	}
	if !strings.Contains(err.Error(), "context length exceeded") {
		t.Errorf("err = %v, want the upstream message", err)
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("err = %v, the key must never be quoted", err)
	}
}

func TestComplete_aLengthFinishIsAnErrorNamingTheBudget(t *testing.T) {
	// The non-streaming twin of the rule above. Every Complete caller parses
	// the reply, and a body cut at the cap would otherwise surface as
	// "not JSON" or, for the judge, as a silent "ask".
	c, _ := fakeUpstreamEnding(t, `{"decision": "comp`, "length")

	out, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})

	var cut *FinishError
	if !errors.As(err, &cut) || cut.Reason != "length" || cut.Completion != 7 {
		t.Fatalf("err = %v, want a FinishError naming length and the completion count", err)
	}
	if out != `{"decision": "comp` {
		t.Errorf("out = %q, want the truncated text handed over with the error", out)
	}
}

// TestChatError_aTransportErrorNamesTheHostNotTheURL: a base URL can carry a
// credential in its query, and a dial failure quotes the URL it dialled.
// llmwire wraps that error with %w, so the url.Error is still there to trim.
func TestChatError_aTransportErrorNamesTheHostNotTheURL(t *testing.T) {
	// Given an endpoint nobody listens on, named with a key in the query
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()
	c := NewClient(Config{BaseURL: addr + "/v1?key=querysecret"}, nil)

	// When
	_, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})

	// Then
	if err == nil {
		t.Fatal("want a transport error against a closed listener")
	}
	if strings.Contains(err.Error(), "querysecret") {
		t.Errorf("err = %v, the query string must not be quoted", err)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("err = %v, want scheme://host kept so an operator sees where it failed", err)
	}
}
