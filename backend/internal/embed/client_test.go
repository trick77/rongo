package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// vecOf builds a distinguishable vector: every component carries the marker, so
// a mispaired result is visible rather than plausible.
func vecOf(marker float32, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = marker
	}
	return v
}

// recordingServer answers /embeddings with respond(inputs) and records every
// request body it saw.
func recordingServer(t *testing.T, respond func(inputs []string) (int, any)) (*httptest.Server, *[][]string) {
	t.Helper()
	var mu sync.Mutex
	var seen [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("request path = %q, want /embeddings", r.URL.Path)
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		seen = append(seen, req.Input)
		mu.Unlock()
		status, body := respond(req.Input)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		switch b := body.(type) {
		case string:
			io.WriteString(w, b)
		default:
			json.NewEncoder(w).Encode(b)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

type respData struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

func TestEmbed_returnsVectorsInInputOrder(t *testing.T) {
	// Given: the endpoint answers OUT OF ORDER, which is allowed by the API and
	// is the whole point of this test. A fake that answers in order would pass
	// whether or not the client realigns, so it would test nothing.
	srv, seen := recordingServer(t, func(inputs []string) (int, any) {
		return 200, map[string]any{"data": []respData{
			{Index: 2, Embedding: vecOf(3, 4)},
			{Index: 0, Embedding: vecOf(1, 4)},
			{Index: 1, Embedding: vecOf(2, 4)},
		}}
	})
	testee := NewClient(Config{BaseURL: srv.URL, Model: "m", Dim: 4}, srv.Client())

	// When
	vecs, err := testee.Embed(context.Background(), []string{"one", "two", "three"})

	// Then
	if err != nil {
		t.Fatalf("Embed() err = %v, want nil", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}
	for i, want := range []float32{1, 2, 3} {
		if vecs[i][0] != want {
			t.Errorf("vector %d = %v, want the one marked %v — results are mispaired with their inputs",
				i, vecs[i][0], want)
		}
	}
	// One request, carrying all three inputs.
	if len(*seen) != 1 {
		t.Fatalf("made %d requests, want 1", len(*seen))
	}
	if got := (*seen)[0]; len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Errorf("request carried %v, want all three inputs in order", got)
	}
}

func TestEmbed_duplicateIndexIsAnError(t *testing.T) {
	// Given: a response whose indexes collide. The count still matches, so a
	// client that trusted the count would store one input's vector twice and
	// leave another chunk holding someone else's.
	srv, _ := recordingServer(t, func(inputs []string) (int, any) {
		return 200, map[string]any{"data": []respData{
			{Index: 0, Embedding: vecOf(1, 4)},
			{Index: 0, Embedding: vecOf(2, 4)},
			{Index: 1, Embedding: vecOf(3, 4)},
		}}
	})
	testee := NewClient(Config{BaseURL: srv.URL, Model: "m", Dim: 4}, srv.Client())

	// When
	_, err := testee.Embed(context.Background(), []string{"a", "b", "c"})

	// Then
	if err == nil {
		t.Fatal("Embed() err = nil, want an error for a duplicated index")
	}
}

func TestEmbed_wrongDimensionIsAnError(t *testing.T) {
	// Given: vec0 would reject this later, at a point far from the cause.
	srv, _ := recordingServer(t, func(inputs []string) (int, any) {
		return 200, map[string]any{"data": []respData{{Index: 0, Embedding: vecOf(1, 3)}}}
	})
	testee := NewClient(Config{BaseURL: srv.URL, Model: "m", Dim: 4}, srv.Client())

	// When
	_, err := testee.Embed(context.Background(), []string{"a"})

	// Then
	if err == nil {
		t.Fatal("Embed() err = nil, want an error for a short vector")
	}
	if !strings.Contains(err.Error(), "4") || !strings.Contains(err.Error(), "3") {
		t.Errorf("error = %q, want it to name both dimensions", err)
	}
}

func TestEmbed_errorCarriesStatusAndACappedBody(t *testing.T) {
	// Given: an endpoint returning a huge error page.
	huge := strings.Repeat("x", 64<<10)
	srv, _ := recordingServer(t, func(inputs []string) (int, any) {
		return http.StatusServiceUnavailable, huge
	})
	testee := NewClient(Config{BaseURL: srv.URL, Model: "m", Dim: 4}, srv.Client())

	// When
	_, err := testee.Embed(context.Background(), []string{"a"})

	// Then
	if err == nil {
		t.Fatal("Embed() err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %.80q…, want it to carry the status", err.Error())
	}
	if len(err.Error()) > 8<<10 {
		t.Errorf("error is %d bytes; the body must be capped before it reaches a log line", len(err.Error()))
	}
}

func TestEmbed_aTransportErrorNeverCarriesTheURL(t *testing.T) {
	// Given: a base URL with a credential in its query string, which some
	// OpenAI-compatible deployments use, pointing at a port nothing listens on.
	// net/http wraps EVERY transport failure in a *url.Error carrying the full
	// URL, and that error is what the caller logs — so the plain wrapped error
	// is a credential in a log line.
	testee := NewClient(Config{
		BaseURL: "http://127.0.0.1:1/v1?api-key=s3cret-key-value",
		Model:   "m", Dim: 4,
		HeartbeatInterval: -1,
	}, &http.Client{Timeout: 2 * time.Second})

	// When
	_, err := testee.Embed(context.Background(), []string{"a"})

	// Then
	if err == nil {
		t.Fatal("Embed() err = nil, want a connection failure")
	}
	if strings.Contains(err.Error(), "s3cret-key-value") || strings.Contains(err.Error(), "api-key") {
		t.Errorf("the error carries the query string: %q", err)
	}
	// The host survives: "which endpoint was unreachable" is the whole
	// diagnostic value of the message.
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("error = %q, want it to name the host it could not reach", err)
	}
}

func TestEmbed_contextCancellationReturnsPromptly(t *testing.T) {
	// Given: an endpoint that never answers.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)
	testee := NewClient(Config{BaseURL: srv.URL, Model: "m", Dim: 4}, srv.Client())
	ctx, cancel := context.WithCancel(context.Background())

	// When
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	done := make(chan error, 1)
	go func() { _, err := testee.Embed(ctx, []string{"a"}); done <- err }()

	// Then
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Embed() err = nil, want the cancellation surfaced")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Embed() did not return after its context was cancelled")
	}
}

func TestEmbed_emptyInputMakesNoRequest(t *testing.T) {
	// Given
	srv, seen := recordingServer(t, func(inputs []string) (int, any) {
		return 200, map[string]any{"data": []respData{}}
	})
	testee := NewClient(Config{BaseURL: srv.URL, Model: "m", Dim: 4}, srv.Client())

	// When
	vecs, err := testee.Embed(context.Background(), nil)

	// Then
	if err != nil || len(vecs) != 0 {
		t.Fatalf("Embed(nil) = %v, %v; want no vectors and no error", vecs, err)
	}
	if len(*seen) != 0 {
		t.Errorf("made %d requests for no input, want 0", len(*seen))
	}
}

func TestEmbed_splitsLargeInputIntoBatchesKeepingOrder(t *testing.T) {
	// Given: more inputs than one request may carry. The concatenation across
	// batches is the second place order can silently break.
	srv, seen := recordingServer(t, func(inputs []string) (int, any) {
		data := make([]respData, len(inputs))
		for i, in := range inputs {
			var n float32
			fmt.Sscanf(in, "text-%f", &n)
			data[len(inputs)-1-i] = respData{Index: i, Embedding: vecOf(n, 4)}
		}
		return 200, map[string]any{"data": data}
	})
	testee := NewClient(Config{BaseURL: srv.URL, Model: "m", Dim: 4}, srv.Client())
	inputs := make([]string, 150)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("text-%d", i)
	}

	// When
	vecs, err := testee.Embed(context.Background(), inputs)

	// Then
	if err != nil {
		t.Fatalf("Embed() err = %v, want nil", err)
	}
	if len(vecs) != len(inputs) {
		t.Fatalf("got %d vectors for %d inputs", len(vecs), len(inputs))
	}
	for i := range inputs {
		if vecs[i][0] != float32(i) {
			t.Fatalf("vector %d carries marker %v, want %d", i, vecs[i][0], i)
		}
	}
	if len(*seen) < 2 {
		t.Errorf("made %d requests for 150 inputs, want several bounded batches", len(*seen))
	}
}
