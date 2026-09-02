package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
)

// streamUpstream streams the given tokens and records the prompt it was sent.
func streamUpstream(t *testing.T, tokens ...string) (*llm.Client, *string, *int) {
	t.Helper()
	var prompt string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		for _, m := range req.Messages {
			prompt += m.Content + "\n"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := http.NewResponseController(w)
		for _, tok := range tokens {
			frame, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": tok}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			_ = fl.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		_ = fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()), &prompt, &calls
}

func twoSources() []Source {
	return []Source{
		{ChunkID: 1, Repo: "peeq", Branch: "master", Path: "backend/internal/playbackgrant/store.go",
			StartLine: 1, EndLine: 30, Text: "func NewGrant() {}", Reason: "hit"},
		{ChunkID: 2, Repo: "peeq", Branch: "master", Path: "backend/internal/httpapi/grant.go",
			StartLine: 5, EndLine: 20, Text: "func issueGrant() {}", Reason: "reference:NewGrant"},
	}
}

func collect(tokens *[]string) func(string) {
	return func(tok string) { *tokens = append(*tokens, tok) }
}

func TestAnswer_streamsAndResolvesTheMarkersItUsed(t *testing.T) {
	// Given
	c, _, _ := streamUpstream(t, "The grant ", "is created in ", "store.go [1].")
	var seen []string

	// When
	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, twoSources(), collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// Then
	if len(seen) < 3 {
		t.Errorf("callbacks = %d, want the answer to arrive in pieces", len(seen))
	}
	if !strings.Contains(got.Text, "store.go [1]") {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Citations) != 1 {
		t.Fatalf("citations = %+v, want only the one marker the answer used", got.Citations)
	}
	cit := got.Citations[0]
	if cit.Marker != 1 || cit.Path != "backend/internal/playbackgrant/store.go" || cit.Branch != "master" {
		t.Errorf("citation = %+v, want it to resolve to source 1 with its branch", cit)
	}
}

func TestAnswer_aMarkerWithNoSourceIsDroppedNotInvented(t *testing.T) {
	// A model that cites [7] with three sources in front of it has made the
	// number up. Emitting a citation for it would put a fabricated reference
	// under an answer — the failure this product can least afford.
	c, _, _ := streamUpstream(t, "This happens in delivery [7].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != 0 {
		t.Errorf("citations = %+v, want none for an invented marker", got.Citations)
	}
}

func TestAnswer_anIndexExpressionInCodeIsNotACitation(t *testing.T) {
	// The DEV prompt asks for short snippets, and `args[1]` inside one is an
	// index expression. Reading it as a marker would put a reference under the
	// answer that the model never made — checkable-looking and false.
	c, _, _ := streamUpstream(t,
		"The call is in store.go [2]:\n\n```go\nname := args[1]\nvalue := parts[1]\n```\n")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceDev, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != 1 || got.Citations[0].Marker != 2 {
		t.Fatalf("citations = %+v, want only the marker outside the code block", got.Citations)
	}
}

func TestAnswer_withoutSourcesItSaysSoAndNeverCallsTheModel(t *testing.T) {
	// "No hit means no hit." Asking the model anyway would get a fluent answer
	// built from nothing but the question and the system prompt.
	c, _, calls := streamUpstream(t, "I suspect that ...")

	got, err := NewAnswerer(c).Answer(context.Background(), "How does shipping work?", AudienceBA, nil, nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if *calls != 0 {
		t.Errorf("the model was called %d times with nothing gathered", *calls)
	}
	if !strings.Contains(strings.ToLower(got.Text), "found nothing") {
		t.Errorf("text = %q, want it to say nothing was found", got.Text)
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %+v, want none", got.Citations)
	}
}

func TestAnswer_theAudienceReachesThePrompt(t *testing.T) {
	// The role changes only this step: language level, depth, whether code is
	// embedded. A prompt that ignored it would make the BA/DEV switch decorative.
	cBA, promptBA, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cBA).Answer(context.Background(), "How?", AudienceBA, twoSources(), nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	cDev, promptDev, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cDev).Answer(context.Background(), "How?", AudienceDev, twoSources(), nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if *promptBA == *promptDev {
		t.Fatal("the BA and DEV prompts are identical; the role switch does nothing")
	}
	if !strings.Contains(*promptBA, "[1]") || !strings.Contains(*promptBA, "playbackgrant/store.go") {
		t.Error("the sources never reached the prompt with their markers")
	}
}
