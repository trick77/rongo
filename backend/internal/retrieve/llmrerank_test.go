package retrieve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/trick77/rongo/internal/llm"
)

func rerankLLM(t *testing.T, reply string, saw *string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if saw != nil && len(req.Messages) > 1 {
			*saw = req.Messages[1].Content
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]int{},
		})
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv)
}

// rerankLLMPicking replies with the number of the result whose header names
// path, read off the prompt it was sent — a model that recognises one file.
func rerankLLMPicking(t *testing.T, path string, saw *string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := req.Messages[len(req.Messages)-1].Content
		if saw != nil {
			*saw = prompt
		}
		reply := `{"relevant":[]}`
		for _, line := range strings.Split(prompt, "\n") {
			if strings.HasPrefix(line, "[") && strings.Contains(line, path) {
				reply = `{"relevant":[` + line[1:strings.IndexByte(line, ']')] + `]}`
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]int{},
		})
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv)
}

func TestLLMRerank_putsWhatTheModelPickedFirstAndKeepsTheRest(t *testing.T) {
	var prompt string
	hits := []Hit{
		{ChunkID: 1, Repo: "peeq", Path: "a.go", RawText: "func a()"},
		{ChunkID: 2, Repo: "peeq", Path: "b.go", RawText: "func b()"},
		{ChunkID: 3, Repo: "peeq", Path: "c.go", Symbol: "c", RawText: "// c is the answer\nfunc c()"},
		{ChunkID: 4, Repo: "peeq", Path: "d.go", RawText: "func d()"},
	}
	r := NewLLMReranker(rerankLLM(t, "```json\n{\"relevant\":[3,1,9,3]}\n```", &prompt), 60)
	got, err := r.Rerank(context.Background(), "what is c?", hits, 3)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(got))
	for i, h := range got {
		ids[i] = h.ChunkID
	}
	// 3 then 1 as the model said (9 out of range and the repeat ignored), then
	// the fused order for the rest, cut to three.
	if len(ids) != 3 || ids[0] != 3 || ids[1] != 1 || ids[2] != 2 {
		t.Errorf("order = %v, want [3 1 2]", ids)
	}
	if !strings.Contains(prompt, "[3] peeq c.go (c)") || !strings.Contains(prompt, "// c is the answer") {
		t.Errorf("the model was not shown the numbered headers and excerpts:\n%s", prompt)
	}
}

func TestLLMRerank_keepsTheFusedOrderWhenTheReplyIsNotJSON(t *testing.T) {
	hits := []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}
	r := NewLLMReranker(rerankLLM(t, "I think result 2 is best.", nil), 60)
	got, err := r.Rerank(context.Background(), "q", hits, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Errorf("a reply that could not be read changed the order: %+v", got)
	}
}

// TestLLMRerank_keepsTheFusedOrderWhenTheCallFails: the gate lane going down
// is not a search failure. The fused list was in hand before the call.
func TestLLMRerank_keepsTheFusedOrderWhenTheCallFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	hits := []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}
	r := NewLLMReranker(fakeLLM(t, srv), 60)
	got, err := r.Rerank(context.Background(), "q", hits, 2)
	if err != nil {
		t.Fatalf("a failed rerank call became a search error: %v", err)
	}
	if len(got) != 2 || got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Errorf("a failed call changed the order: %+v", got)
	}
}

// TestLLMRerank_reportsACancelledContextAsSuch: a reader who left is not a
// gate-lane outage, and the search reports the cancellation like every
// other step.
func TestLLMRerank_reportsACancelledContextAsSuch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewLLMReranker(fakeLLM(t, srv), 60)
	if _, err := r.Rerank(ctx, "q", []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}, 1); err == nil {
		t.Fatal("a cancelled context was answered with the fused order")
	}
}

// TestExcerpt_isRuneSafeAndCutsAtALineBreak: the excerpt is measured in runes,
// so a cut never splits an umlaut, and it prefers the last line break in the
// back half of the window so the model reads whole lines.
func TestExcerpt_isRuneSafeAndCutsAtALineBreak(t *testing.T) {
	t.Run("a cut never splits a rune", func(t *testing.T) {
		got := excerpt(strings.Repeat("ä", 50), 10)
		if !utf8.ValidString(got) {
			t.Fatalf("excerpt cut inside a rune: %q", got)
		}
		if got != strings.Repeat("ä", 10)+"…" {
			t.Errorf("excerpt = %q, want ten runes and the ellipsis", got)
		}
	})
	t.Run("a line break in the back half wins", func(t *testing.T) {
		s := strings.Repeat("a", 6) + "\n" + strings.Repeat("b", 20)
		got := excerpt(s, 10)
		if got != strings.Repeat("a", 6)+"…" {
			t.Errorf("excerpt = %q, want the cut at the newline", got)
		}
	})
	t.Run("a line break in the front half loses", func(t *testing.T) {
		s := "ab\n" + strings.Repeat("c", 30)
		got := excerpt(s, 10)
		if got != "ab\n"+strings.Repeat("c", 7)+"…" {
			t.Errorf("excerpt = %q, want the full window", got)
		}
	})
	t.Run("a single overlong line cuts at n", func(t *testing.T) {
		got := excerpt(strings.Repeat("x", 100), 10)
		if got != strings.Repeat("x", 10)+"…" {
			t.Errorf("excerpt = %q, want ten runes and the ellipsis", got)
		}
	})
	t.Run("text that fits comes back whole", func(t *testing.T) {
		if got := excerpt("  füüf\nlines  ", 10); got != "füüf\nlines" {
			t.Errorf("excerpt = %q, want the whole trimmed text", got)
		}
	})
}

// TestLLMRerank_headerCarriesTheStartLine: two chunks of one file differ by
// where they start, and the model cannot tell them apart without it.
func TestLLMRerank_headerCarriesTheStartLine(t *testing.T) {
	var prompt string
	hits := []Hit{
		{ChunkID: 1, Repo: "peeq", Path: "a.go", Symbol: "a", StartLine: 42, RawText: "func a()"},
		{ChunkID: 2, Repo: "peeq", Path: "b.go", RawText: "func b()"},
	}
	r := NewLLMReranker(rerankLLM(t, `{"relevant":[1]}`, &prompt), 60)
	if _, err := r.Rerank(context.Background(), "q", hits, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "[1] peeq a.go:42 (a)") {
		t.Errorf("the start line is missing from the header:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[2] peeq b.go\n") {
		t.Errorf("a chunk starting at line zero got a colon:\n%s", prompt)
	}
}

// TestLLMRerank_excerptWidthIsAField: the harness widens what the model reads;
// a line past the default window is invisible at 240 and visible at 800.
func TestLLMRerank_excerptWidthIsAField(t *testing.T) {
	body := strings.Repeat("filler line\n", 50) + "THE MARKER\n" + strings.Repeat("tail line\n", 50)
	hits := []Hit{{ChunkID: 1, Repo: "peeq", Path: "a.go", RawText: body}, {ChunkID: 2}}

	var narrow string
	r := NewLLMReranker(rerankLLM(t, `{"relevant":[1]}`, &narrow), 60)
	if _, err := r.Rerank(context.Background(), "q", hits, 2); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(narrow, "THE MARKER") {
		t.Errorf("the default width already reaches the marker; the test proves nothing")
	}

	var wide string
	r = NewLLMReranker(rerankLLM(t, `{"relevant":[1]}`, &wide), 60)
	r.Excerpt = 800
	if _, err := r.Rerank(context.Background(), "q", hits, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wide, "THE MARKER") {
		t.Errorf("Excerpt = 800 did not widen what the model was shown:\n%s", wide)
	}
}

// TestLLMRerank_replyCapGrowsWithThePool: a pool of a hundred needs more than
// the 256-token floor, or the reply ends with finish_reason=length, which is
// an error, a warning and the fused order.
func TestLLMRerank_replyCapGrowsWithThePool(t *testing.T) {
	var capSeen float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// The gate profile names its own spelling of the cap parameter.
		for _, key := range []string{"max_completion_tokens", "max_tokens"} {
			if v, ok := body[key].(float64); ok {
				capSeen = v
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"relevant":[1]}`}, "finish_reason": "stop"}},
			"usage":   map[string]int{},
		})
	}))
	t.Cleanup(srv.Close)

	hits := make([]Hit, 100)
	for i := range hits {
		hits[i] = Hit{ChunkID: int64(i + 1), Repo: "peeq", Path: "a.go"}
	}
	r := NewLLMReranker(fakeLLM(t, srv), 100)
	if _, err := r.Rerank(context.Background(), "q", hits, 20); err != nil {
		t.Fatal(err)
	}
	if capSeen < 464 {
		t.Errorf("reply cap = %v for a pool of 100, want at least 464", capSeen)
	}

	small := []Hit{{ChunkID: 1}, {ChunkID: 2}}
	r = NewLLMReranker(fakeLLM(t, srv), 60)
	if _, err := r.Rerank(context.Background(), "q", small, 2); err != nil {
		t.Fatal(err)
	}
	if capSeen != rerankMaxTokens {
		t.Errorf("reply cap = %v for two hits, want the floor %d", capSeen, rerankMaxTokens)
	}
}

// fakeLLM is the model client pointed at a test server. With BaseURL set,
// llm.NewClient consults no environment variable, so the only way this can
// fail is a bug in the constructor.
func fakeLLM(t testing.TB, srv *httptest.Server) *llm.Client {
	t.Helper()
	c, err := llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatalf("llm.NewClient: %v", err)
	}
	return c
}
