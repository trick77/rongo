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
	return streamUpstreamEnding(t, "", tokens)
}

// streamUpstreamEnding is streamUpstream with the finish_reason the stream
// ends on; "" sends none.
func streamUpstreamEnding(t *testing.T, finishReason string, tokens []string) (*llm.Client, *string, *int) {
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
		if finishReason != "" {
			end, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finishReason}},
			})
			fmt.Fprintf(w, "data: %s\n\n", end)
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
	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), collect(&seen))
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

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != 0 {
		t.Errorf("citations = %+v, want none for an invented marker", got.Citations)
	}
}

func TestAnswer_aGroupedMarkerCountsForEachNumberInIt(t *testing.T) {
	// A claim resting on several sources comes out as [1, 2]. Read as one
	// marker it matches nothing, and both sources vanish from the panel while
	// the text still shows the brackets - the reader sees a citation that
	// leads nowhere.
	c, _, _ := streamUpstream(t, "Compared on poll [1, 2], and again [2,1].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != 2 || got.Citations[0].Marker != 1 || got.Citations[1].Marker != 2 {
		t.Fatalf("citations = %+v, want markers 1 and 2 once each", got.Citations)
	}
}

func TestAnswer_anInventedNumberInsideAGroupIsDroppedAlone(t *testing.T) {
	c, _, _ := streamUpstream(t, "Compared on poll [1, 9].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(got.Citations) != 1 || got.Citations[0].Marker != 1 {
		t.Fatalf("citations = %+v, want only the real marker of the group", got.Citations)
	}
}

func TestAnswer_anIndexExpressionInCodeIsNotACitation(t *testing.T) {
	// The DEV prompt asks for short snippets, and `args[1]` inside one is an
	// index expression. Reading it as a marker would put a reference under the
	// answer that the model never made — checkable-looking and false.
	c, _, _ := streamUpstream(t,
		"The call is in store.go [2]:\n\n```go\nname := args[1]\nvalue := parts[1]\n```\n")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if !strings.Contains(got.Text, "store.go [1]:") || !strings.Contains(got.Text, "args[1]") {
		t.Errorf("text = %q, want the prose marker renumbered and the code untouched", got.Text)
	}
	if len(got.Citations) != 1 || got.Citations[0].Marker != 1 || got.Citations[0].Path != "backend/internal/httpapi/grant.go" {
		t.Fatalf("citations = %+v, want only the marker outside the code block", got.Citations)
	}
}

func TestAnswer_markersAreRenumberedInOrderOfFirstAppearance(t *testing.T) {
	// The prompt numbers a hundred sources and the model cites three of them,
	// so the reader sees [107]. The answer reads 1, 2, 3 in the order the
	// markers appear, and the stream carries the same text as the record:
	// a marker split across tokens is held back until it is complete.
	c, _, _ := streamUpstream(t, "Issued in grant.go [", "2", "], stored [1] and again [2].")
	var seen []string

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "Issued in grant.go [1], stored [2] and again [1]." {
		t.Errorf("text = %q", got.Text)
	}
	if strings.Join(seen, "") != got.Text {
		t.Errorf("streamed %q, stored %q; the reader must see the record", strings.Join(seen, ""), got.Text)
	}
	if len(got.Citations) != 2 || got.Citations[0].Marker != 1 || got.Citations[1].Marker != 2 {
		t.Fatalf("citations = %+v, want markers 1 and 2", got.Citations)
	}
	if got.Citations[0].Path != "backend/internal/httpapi/grant.go" || got.Citations[1].Path != "backend/internal/playbackgrant/store.go" {
		t.Errorf("citations = %+v, want 1 to be the source the model called [2]", got.Citations)
	}
}

func TestAnswer_aGroupedMarkerIsRenumberedPerNumber(t *testing.T) {
	c, _, _ := streamUpstream(t, "Compared on poll [2, 1] and [9, 2].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// An invented number stays as it came, so the UI drops it to plain text.
	if got.Text != "Compared on poll [1, 2] and [9, 1]." {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Citations) != 2 {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

func TestAnswer_aMarkerInsideInlineCodeIsNotRenumbered(t *testing.T) {
	// The span is split across tokens: the line is held back until the
	// closing backtick says it is code.
	c, _, _ := streamUpstream(t, "Use `args[", "2]` as in grant.go [2].")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "Use `args[2]` as in grant.go [1]." {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Citations) != 1 || got.Citations[0].Marker != 1 {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

func TestAnswer_anUnclosedBacktickIsProseAtTheEnd(t *testing.T) {
	// withoutCode reads an inline span only when it closes on the same line;
	// a stray backtick with a marker after it is prose, and the marker counts.
	c, _, _ := streamUpstream(t, "A stray ` and then [2]")

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got.Text != "A stray ` and then [1]" {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Citations) != 1 {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

func TestAnswer_aCutAnswerIsStillRenumberedAndFlushed(t *testing.T) {
	c, _, _ := streamUpstreamEnding(t, "length", []string{"Stored [2] and then [", "1"})
	var seen []string

	got, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// The half-written marker at the cut is text, and it reaches the reader.
	if got.Text != "Stored [1] and then [1" || strings.Join(seen, "") != got.Text {
		t.Errorf("text = %q, streamed %q", got.Text, strings.Join(seen, ""))
	}
	if len(got.Citations) != 1 || got.Citations[0].Path != "backend/internal/httpapi/grant.go" {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

func TestAnswer_withoutSourcesItSaysSoAndNeverCallsTheModel(t *testing.T) {
	// "No hit means no hit." Asking the model anyway would get a fluent answer
	// built from nothing but the question and the system prompt.
	c, _, calls := streamUpstream(t, "I suspect that ...")

	got, err := NewAnswerer(c).Answer(context.Background(), "How does shipping work?", AudienceBA, LanguageEN, nil, nil)
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
	if _, err := NewAnswerer(cBA).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	cDev, promptDev, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cDev).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if *promptBA == *promptDev {
		t.Fatal("the BA and DEV prompts are identical; the role switch does nothing")
	}
	if !strings.Contains(*promptBA, "[1]") || !strings.Contains(*promptBA, "playbackgrant/store.go") {
		t.Error("the sources never reached the prompt with their markers")
	}
	// The UI colours a fence by its tag and guesses nothing; an untagged
	// fence stays plain. Only the DEV answer carries code; both may carry the
	// one fence that is not code, the diagram.
	if !strings.Contains(*promptDev, "```go") {
		t.Error("the DEV prompt does not ask for a language tag on fenced code")
	}
	if strings.Contains(*promptBA, "```go") {
		t.Error("the BA prompt talks about fenced code, but a BA answer carries none")
	}
	for name, p := range map[string]string{"BA": *promptBA, "DEV": *promptDev} {
		if !strings.Contains(p, "```diagram") || !strings.Contains(p, `"src":[1]`) {
			t.Errorf("the %s prompt does not name the diagram fence and its src arrays", name)
		}
	}
	// The diagram rule follows the audience block, so "the audience rules
	// above" are the ones the model just read.
	if strings.Index(*promptBA, "Audience: business analyst") > strings.Index(*promptBA, "```diagram") {
		t.Error("the diagram rule comes before the audience block it refers to")
	}
}

func TestAnswer_anEmptyCompletionIsAnErrorNotAnAnswer(t *testing.T) {
	// An upstream that ends cleanly without one content delta must not become
	// a finished turn with nothing in it: the reader would see a Done mark over
	// an empty answer and nothing would be logged.
	c, _, _ := streamUpstream(t)
	a := NewAnswerer(c)

	_, err := a.Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), nil)

	if err == nil {
		t.Fatal("Answer: nil error on an empty completion")
	}
	if !strings.Contains(err.Error(), "no answer text") {
		t.Errorf("err = %v, want it to say the model wrote nothing", err)
	}
}

func TestAnswer_aCutAnswerKeepsWhatTheReaderAlreadySaw(t *testing.T) {
	// finish_reason=length after content: the text streamed to the browser
	// must not vanish from the record. Empty is a failure; truncated is an
	// answer with a log line.
	c, _, _ := streamUpstreamEnding(t, "length", []string{"The grant ", "is created in store.go [1]."})
	a := NewAnswerer(c)

	got, err := a.Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), nil)

	if err != nil {
		t.Fatalf("Answer: %v, want the partial text kept", err)
	}
	if !strings.Contains(got.Text, "store.go [1]") || len(got.Citations) != 1 {
		t.Errorf("answer = %+v, want the text that arrived, with its citation", got)
	}
}

func TestAnswer_aCutAnswerWithNoTextIsStillAnError(t *testing.T) {
	c, _, _ := streamUpstreamEnding(t, "length", nil)

	_, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceDev, LanguageEN, twoSources(), nil)

	if err == nil || !strings.Contains(err.Error(), "length") {
		t.Fatalf("err = %v, want the length failure surfaced", err)
	}
}
