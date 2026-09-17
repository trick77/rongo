package ask

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

const reworkReply = `{"intent":"rework","terms":[],"code_terms":[],"repos":[]}`

// reworkUpstream is twoStepUpstream with the answer call's prompt recorded:
// the rework tests are about what the answering model was handed.
func reworkUpstream(t *testing.T, understanding string) (*llm.Client, *string) {
	t.Helper()
	var prompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": understanding}}},
			})
			return
		}
		for _, m := range req.Messages {
			prompt += m.Content + "\n"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, []string{"Kurz: ", "[2]."}, "")
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv), &prompt
}

func reworkThread() Thread {
	return Thread{
		Question: "How is a playback grant issued?",
		Answer:   "A grant is created by NewGrant [1] and handed out by the HTTP layer [2][1].\n\n```mermaid\nflowchart LR\n```\n",
		Sources:  twoSources(),
		// The record holds exactly what resolved: a whole basis.
		SourcesTotal: 2,
	}
}

func reworkPipeline(t *testing.T, c *llm.Client) *Pipeline {
	t.Helper()
	search := withSearcher(func(retrieve.Query) ([]retrieve.Hit, error) {
		t.Fatal("a rework must not search")
		return nil, nil
	})
	f := pipelineFakes{router: &fakeRouter{}}
	search(&f)
	return NewPipeline(c, f.search, NewGatherer(gatherDB(t), GatherOptions{MaxHops: 1, TokenBudget: 5000}), f.router)
}

// TestRunReworksThePreviousAnswerWithoutSearching: "summarize" is answered
// from the previous answer and its own sources. Nothing is searched, the
// answer prompt carries the previous text whole with its markers stripped,
// and the record's basis is the previous turn's.
func TestRunReworksThePreviousAnswerWithoutSearching(t *testing.T) {
	c, prompt := reworkUpstream(t, reworkReply)
	p := reworkPipeline(t, c)
	var steps []string

	got, clar, err := p.Run(context.Background(), "summarize", AudienceBA, LanguageEN, reworkThread(),
		Events{OnStatus: func(s string) { steps = append(steps, s) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if clar != nil {
		t.Fatal("a rework never asks")
	}
	for _, s := range steps {
		if s == "searching" || s == "routing" || s == "gathering" {
			t.Errorf("a rework ran %q; steps: %v", s, steps)
		}
	}
	if !strings.Contains(*prompt, "Previous answer:\nA grant is created by NewGrant  and handed out by the HTTP layer .") {
		t.Errorf("the previous answer, markers stripped, never reached the prompt:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "flowchart LR") {
		t.Errorf("the previous answer's fence must stay, it is part of what is reworked:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "Instruction: summarize") {
		t.Errorf("the instruction never reached the prompt:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "[2] peeq backend/internal/httpapi/grant.go:5-20") {
		t.Errorf("the previous turn's sources are not in front of the model:\n%s", *prompt)
	}
	if strings.Contains(*prompt, "This is a follow-up") {
		t.Errorf("the follow-up rule forbids restating, which is the whole task:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "written again in another") {
		t.Errorf("the rework rule is missing:\n%s", *prompt)
	}
	if len(got.Sources) != 2 || got.Sources[1].ChunkID != 2 {
		t.Errorf("sources = %+v, want the previous turn's", got.Sources)
	}
	// The model cited [2] from the rendered list; the record resolves it.
	if len(got.Citations) != 1 || got.Citations[0].Path != "backend/internal/httpapi/grant.go" {
		t.Errorf("citations = %+v, want the one the rework cited", got.Citations)
	}
}

// TestRunTreatsAReworkOnAFirstTurnAsAnOrdinaryQuestion: the guard is the
// pipeline's. A first turn has nothing to rework whatever the model said.
func TestRunTreatsAReworkOnAFirstTurnAsAnOrdinaryQuestion(t *testing.T) {
	searched := false
	p := newTestPipeline(t, withSearcher(func(retrieve.Query) ([]retrieve.Hit, error) {
		searched = true
		return nil, nil
	}))
	p.understander = NewUnderstander(twoStepUpstream(t, reworkReply, "x"))

	got, _, err := p.Run(context.Background(), "summarize", AudienceBA, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !searched {
		t.Error("a first turn must run the ordinary path, rework or not")
	}
	// The record too: a re-explain keys on the stored intent to rework again.
	if got.Scope.Intent == IntentRework {
		t.Errorf("scope.Intent = %q on a turn that ran the ordinary path", got.Scope.Intent)
	}
}

// TestRunRefusesAReworkWhoseBasisIsGone: a re-index between the turns took
// some of the previous answer's sources. Neither a summary of the survivors
// nor a fresh search: the turn fails with ErrBasisGone, and the handler says
// which.
func TestRunRefusesAReworkWhoseBasisIsGone(t *testing.T) {
	c, prompt := reworkUpstream(t, reworkReply)
	p := reworkPipeline(t, c)
	th := reworkThread()
	th.Sources = th.Sources[:1]

	_, _, err := p.Run(context.Background(), "summarize", AudienceBA, LanguageEN, th, Events{})
	if !errors.Is(err, ErrBasisGone) {
		t.Fatalf("err = %v, want ErrBasisGone", err)
	}
	if *prompt != "" {
		t.Errorf("nothing may be answered from a partial basis:\n%s", *prompt)
	}

	th.Sources = nil
	if _, _, err := p.Run(context.Background(), "summarize", AudienceBA, LanguageEN, th, Events{}); !errors.Is(err, ErrBasisGone) {
		t.Fatalf("err = %v, want ErrBasisGone with no sources left at all", err)
	}
}

// TestRunTreatsAReworkOfAnAnswerWithoutABasisAsAnOrdinaryQuestion: the
// previous turn was "nothing found" — answered, so it is the antecedent, and
// without a source to its name. Nothing was ever indexed for it, so "no
// longer indexed" would be false; the turn runs the ordinary path.
func TestRunTreatsAReworkOfAnAnswerWithoutABasisAsAnOrdinaryQuestion(t *testing.T) {
	searched := false
	p := newTestPipeline(t, withSearcher(func(retrieve.Query) ([]retrieve.Hit, error) {
		searched = true
		return nil, nil
	}))
	p.understander = NewUnderstander(twoStepUpstream(t, reworkReply, "x"))
	th := Thread{Question: "How is X priced?", Answer: "Nothing found. Terms tried: X."}

	if _, _, err := p.Run(context.Background(), "summarize", AudienceBA, LanguageEN, th, Events{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !searched {
		t.Error("an antecedent with no basis has nothing to rework; the ordinary path must run")
	}
}

// TestReworkIsTheSameLaneRunTakes: the handler's re-explain of a rework row
// enters through Rework and has to land in the same prompt.
func TestReworkIsTheSameLaneRunTakes(t *testing.T) {
	c, prompt := reworkUpstream(t, reworkReply)
	p := reworkPipeline(t, c)

	got, err := p.Rework(context.Background(), "summarize", AudienceDev, LanguageEN, reworkThread(), Scope{}, Events{})
	if err != nil {
		t.Fatalf("Rework: %v", err)
	}
	if !strings.Contains(*prompt, "Instruction: summarize") || strings.Contains(*prompt, "This is a follow-up") {
		t.Errorf("Rework did not build the rework prompt:\n%s", *prompt)
	}
	if len(got.Sources) != 2 {
		t.Errorf("sources = %+v, want the previous turn's", got.Sources)
	}
}

// TestStripMarkersOutsideFences: [0] in a quoted code block is an index
// expression, not a citation, and "shorter" must not condense broken code.
func TestStripMarkersOutsideFences(t *testing.T) {
	in := "Read [1][2].\n\n```go\nx := parts[0]\n```\nThen [3].\n```\ny[1]"
	want := "Read .\n\n```go\nx := parts[0]\n```\nThen .\n```\ny[1]"
	if got := stripMarkersOutsideFences(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReworkMeasuresTheSourcesOnTheirOwn: the previous turn in the user
// message is neither the question nor the code, and the split says so.
func TestReworkMeasuresTheSourcesOnTheirOwn(t *testing.T) {
	c, _ := reworkUpstream(t, reworkReply)
	th := reworkThread()
	th.Answer = strings.Repeat("A long previous answer. ", 200)
	got, err := NewAnswerer(c).Rework(context.Background(), "summarize", AudienceBA, LanguageEN, th, Scope{}, nil)
	if err != nil {
		t.Fatalf("Rework: %v", err)
	}
	var list strings.Builder
	renderSourceList(&list, th.Sources, nil)
	if got.Prompt.Sources != estimateTokens(list.String()) || got.Prompt.Question != estimateTokens("summarize") {
		t.Errorf("prompt parts = %+v, want the sources and the instruction measured alone", got.Prompt)
	}
}

// TestUnderstandNamesRework: the intent exists in the prompt, with the rule
// that it needs a previous turn.
func TestUnderstandNamesRework(t *testing.T) {
	if !strings.Contains(understandSystem, `"rework"`) || !strings.Contains(understandSystem, "It exists only when a previous turn is above") {
		t.Error("the understanding prompt must define rework and tie it to a previous turn")
	}
}
