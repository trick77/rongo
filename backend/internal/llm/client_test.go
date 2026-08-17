package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captured is one request body as the fake upstream saw it.
type captured struct {
	Model               string          `json:"model"`
	Messages            []Message       `json:"messages"`
	Stream              bool            `json:"stream"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	Thinking            *thinkingOption `json:"thinking"`
}

// fakeUpstream answers one chat completion and records what it was asked.
func fakeUpstream(t *testing.T, reply string) (*Client, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, got); err != nil {
			t.Errorf("upstream got unparseable body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
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
	c, got := fakeUpstream(t, "die Antwort")

	// When
	out, usage := ask(t, c)

	// Then
	if out != "die Antwort" {
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
func streamingUpstream(t *testing.T, tokens []string) *Client {
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
		usage, _ := json.Marshal(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7},
		})
		fmt.Fprintf(w, "data: %s\n\n", usage)
		fmt.Fprint(w, "data: [DONE]\n\n")
		_ = fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return NewClient(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
}

func TestStream_deliversTokensOneByOne(t *testing.T) {
	// Given
	c := streamingUpstream(t, []string{"Der ", "Versand ", "läuft"})
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
	if strings.Join(seen, "") != "Der Versand läuft" {
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
			"choices": []any{map[string]any{"delta": map[string]any{"content": "Der "}}},
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
	if len(seen) != 1 || seen[0] != "Der " {
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
