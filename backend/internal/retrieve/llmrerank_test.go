package retrieve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client())
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
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client())
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
	r := NewLLMReranker(llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()), 60)
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
	r := NewLLMReranker(llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()), 60)
	if _, err := r.Rerank(ctx, "q", []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}, 1); err == nil {
		t.Fatal("a cancelled context was answered with the fused order")
	}
}
