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
	"github.com/trick77/rongo/internal/memory"
	"github.com/trick77/rongo/internal/retrieve"
)

const flowchartDirective = `{"intent":"memory","terms":[],"code_terms":[],"repos":[],
  "memory":"Never draw flowchart diagrams.","memory_scope":"","memory_replaces":["3"],"memory_removes":[]}`

func withMemory(rows ...memory.Row) context.Context {
	return memory.With(context.Background(), memory.NewHolder(rows))
}

// TestUnderstand_asksForAMemoryOnlyWhenTheDeploymentKeepsOne: the prompt
// gains the memory fields with a holder on the context and is byte for byte
// the old one without — that is what keeps the eval baseline comparable.
func TestUnderstand_asksForAMemoryOnlyWhenTheDeploymentKeepsOne(t *testing.T) {
	c, _, prompt := modelUpstream(t, appleTVReply)
	if _, err := NewUnderstander(c).Understand(context.Background(), "How?", Thread{}, nil); err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if strings.Contains(*prompt, "memory") {
		t.Fatalf("memory off, yet the prompt asks for one:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, `"changes" or "rework"`) {
		t.Fatalf("the intent line changed with memory off:\n%s", *prompt)
	}

	c, _, prompt = modelUpstream(t, appleTVReply)
	if _, err := NewUnderstander(c).Understand(withMemory(memory.Row{ID: 7, Text: "Keep it short."}), "How?", Thread{}, nil); err != nil {
		t.Fatalf("Understand: %v", err)
	}
	for _, want := range []string{`"rework" or "memory"`, "memory_replaces", "[7] Keep it short.", `"memory" is the intent`} {
		if !strings.Contains(*prompt, want) {
			t.Errorf("memory on, prompt lacks %q:\n%s", want, *prompt)
		}
	}
	if strings.Index(*prompt, "Question: How?") > strings.Index(*prompt, "[7] Keep it short.") {
		t.Errorf("the saved rules must follow the question, not precede it:\n%s", *prompt)
	}
}

func TestUnderstand_readsTheMemoryFieldsToleratingStringIds(t *testing.T) {
	c, _, _ := modelUpstream(t, flowchartDirective)
	got, err := NewUnderstander(c).Understand(withMemory(), "Zeig mir nie wieder Flowcharts.", Thread{}, nil)
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if got.Intent != IntentMemory || got.Memory != "Never draw flowchart diagrams." {
		t.Fatalf("got %+v", got)
	}
	d := got.Directive()
	if len(d.Replaces) != 1 || d.Replaces[0] != 3 || len(d.Removes) != 0 {
		t.Fatalf("directive = %+v", d)
	}
	// Garbage in the id lists costs the ids, never the understanding.
	c, _, _ = modelUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[],"memory_replaces":"nope","memory_removes":[{"id":1}]}`)
	got, err = NewUnderstander(c).Understand(withMemory(), "How?", Thread{}, nil)
	if err != nil || got.Intent != "how" || len(got.MemoryReplaces) != 0 || len(got.MemoryRemoves) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// memoryUpstream answers the understanding call with the directive and
// counts the streamed answer calls, so a test can say the turn made none.
func memoryUpstream(t *testing.T, understanding string, answerTokens ...string) (*llm.Client, *int, *string) {
	t.Helper()
	var streams int
	var system string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
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
		streams++
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, answerTokens, "")
	}))
	t.Cleanup(srv.Close)
	return fakeLLM(t, srv), &streams, &system
}

func TestPipeline_aDirectiveAloneIsRememberedAndAnsweredWithoutAModel(t *testing.T) {
	db := gatherDB(t)
	c, streams, _ := memoryUpstream(t, flowchartDirective, "never")
	p := NewPipeline(c, &fakeSearch{}, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	var got memory.Directive
	var steps []string
	details := map[string]map[string]any{}
	ev := Events{
		OnStatus: func(s string) { steps = append(steps, s) },
		OnDetail: func(s string, d map[string]any) { details[s] = d },
		OnMemory: func(d memory.Directive) (memory.Added, error) {
			got = d
			return memory.Added{Row: memory.Row{ID: 9, Text: d.Text, ScopeLive: true}, Replaced: []string{"Always draw one."}, Deleted: []int64{3}}, nil
		},
	}
	holder := memory.NewHolder([]memory.Row{{ID: 3, Text: "Always draw one."}})

	answer, clar, err := p.Run(memory.With(context.Background(), holder), "Zeig mir nie wieder Flowcharts.", AudienceBA, LanguageDE, Thread{}, ev)
	if err != nil || clar != nil {
		t.Fatalf("Run: %v %v", err, clar)
	}

	if got.Text != "Never draw flowchart diagrams." || len(got.Replaces) != 1 {
		t.Fatalf("directive handed to the caller = %+v", got)
	}
	if *streams != 0 {
		t.Fatalf("%d answer calls, want none for a turn that was only a rule", *streams)
	}
	if answer.Scope.Intent != IntentMemory || len(answer.Sources) != 0 {
		t.Fatalf("answer = %+v", answer)
	}
	if !strings.Contains(answer.Text, `Notiert: "Never draw flowchart diagrams". `) || !strings.Contains(answer.Text, `Ersetzt: "Always draw one".`) {
		t.Fatalf("text = %q", answer.Text)
	}
	if strings.Join(steps, ",") != "understanding,remembering" {
		t.Fatalf("steps = %v", steps)
	}
	if d := details["remembering"]; d["memory"] != "Never draw flowchart diagrams." {
		t.Fatalf("remembering detail = %v", d)
	}
	// The holder was updated in place: the old rule went, the new one leads.
	rows := holder.Rows()
	if len(rows) != 1 || rows[0].ID != 9 {
		t.Fatalf("holder rows = %+v", rows)
	}
}

func TestPipeline_aDirectiveBesideAQuestionIsAppliedToThatSameAnswer(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "a.go", 0, 1, 10, "f", "func f() {}")
	reply := `{"intent":"how","terms":["t"],"code_terms":["f"],"repos":[],"memory":"Never draw flowchart diagrams."}`
	c, streams, system := memoryUpstream(t, reply, "So [1].")
	p := NewPipeline(c, &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	ev := Events{OnMemory: func(d memory.Directive) (memory.Added, error) {
		return memory.Added{Row: memory.Row{ID: 1, Text: d.Text, ScopeLive: true}}, nil
	}}

	answer, _, err := p.Run(withMemory(), "How does f work, and never show me flowcharts again?", AudienceBA, LanguageEN, Thread{}, ev)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *streams != 1 || answer.Scope.Intent != "how" {
		t.Fatalf("streams = %d, intent = %q: the question must still be answered", *streams, answer.Scope.Intent)
	}
	if !strings.Contains(*system, "- Never draw flowchart diagrams.\n") {
		t.Fatalf("the rule given in the same breath is missing from the prompt:\n%s", *system)
	}
	if answer.Memories != 1 {
		t.Fatalf("Memories = %d", answer.Memories)
	}
}

func TestPipeline_aMemoryIntentWithNothingToKeepRunsAsAQuestion(t *testing.T) {
	db := gatherDB(t)
	c, streams, _ := memoryUpstream(t, `{"intent":"memory","terms":[],"code_terms":[],"repos":[],"memory":""}`, "x")
	p := NewPipeline(c, &fakeSearch{}, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	// With memory off entirely, and with it on but the model naming no
	// rule, the intent is dropped and the question searched.
	for _, ctx := range []context.Context{context.Background(), withMemory()} {
		answer, _, err := p.Run(ctx, "Never?", AudienceBA, LanguageEN, Thread{}, Events{OnMemory: func(memory.Directive) (memory.Added, error) {
			t.Fatal("nothing to remember, yet the caller was asked to")
			return memory.Added{}, nil
		}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if answer.Scope.Intent == IntentMemory || !strings.Contains(answer.Text, "found nothing") {
			t.Fatalf("answer = %+v", answer)
		}
	}
	if *streams != 0 {
		t.Fatalf("nothing found must not call the model")
	}
}

func TestPipeline_aFullMemoryRefusesTheRuleAndSaysSo(t *testing.T) {
	db := gatherDB(t)
	c, _, _ := memoryUpstream(t, flowchartDirective)
	p := NewPipeline(c, &fakeSearch{}, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	var detail map[string]any
	ev := Events{
		OnDetail: func(s string, d map[string]any) {
			if s == "remembering" {
				detail = d
			}
		},
		OnMemory: func(memory.Directive) (memory.Added, error) { return memory.Added{}, memory.ErrFull },
	}
	answer, _, err := p.Run(withMemory(), "Nie wieder Flowcharts.", AudienceBA, LanguageEN, Thread{}, ev)
	if err != nil {
		t.Fatalf("a full memory is not a failed turn: %v", err)
	}
	if !strings.Contains(answer.Text, "Memory is full: 40 instructions") {
		t.Fatalf("text = %q", answer.Text)
	}
	if detail["refused"] != "full" {
		t.Fatalf("detail = %v", detail)
	}

	// A write that failed on a turn that was only the rule fails the turn:
	// there is nothing else to deliver.
	ev.OnMemory = func(memory.Directive) (memory.Added, error) { return memory.Added{}, errors.New("disk") }
	if _, _, err := p.Run(withMemory(), "Nie wieder Flowcharts.", AudienceBA, LanguageEN, Thread{}, ev); err == nil || !strings.Contains(err.Error(), "disk") {
		t.Fatalf("err = %v", err)
	}
	if detail["refused"] != "failed" {
		t.Fatalf("detail = %v", detail)
	}
}

func TestPipeline_aFailedWriteBesideAQuestionIsATraceLineNotAFailedTurn(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "a.go", 0, 1, 10, "f", "func f() {}")
	reply := `{"intent":"how","terms":["t"],"code_terms":["f"],"repos":[],"memory":"Never draw flowchart diagrams."}`
	c, streams, _ := memoryUpstream(t, reply, "So [1].")
	p := NewPipeline(c, &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	var detail map[string]any
	ev := Events{
		OnDetail: func(s string, d map[string]any) {
			if s == "remembering" {
				detail = d
			}
		},
		OnMemory: func(memory.Directive) (memory.Added, error) { return memory.Added{}, errors.New("disk") },
	}

	answer, _, err := p.Run(withMemory(), "How does f work, and never show me flowcharts again?", AudienceBA, LanguageEN, Thread{}, ev)
	if err != nil {
		t.Fatalf("the question died of an aside: %v", err)
	}
	if *streams != 1 || !strings.Contains(answer.Text, "So [1].") {
		t.Fatalf("streams = %d, text = %q", *streams, answer.Text)
	}
	if detail["refused"] != "failed" {
		t.Fatalf("detail = %v, want the trace to say the rule was not kept", detail)
	}
}

func TestMemoryAnswer_aRuleWithQuotesIsNotEscaped(t *testing.T) {
	added := &memory.Added{Row: memory.Row{ID: 1, Text: `Do not mention "lerb-chooser-ui".`}, Removed: []string{`Skip "tests".`}}
	text := MemoryAnswer(LanguageEN, added, false)
	if strings.Contains(text, `\"`) {
		t.Fatalf("Go escapes reached the reader: %q", text)
	}
	if !strings.Contains(text, `Noted: "Do not mention "lerb-chooser-ui"".`) || !strings.Contains(text, `Forgotten: "Skip "tests"".`) {
		t.Fatalf("text = %q", text)
	}
}

func TestAnswer_theReadersRulesCloseThePromptAheadOfTheLanguage(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "So [1].")
	rows := []memory.Row{{ID: 1, Text: "Never draw flowchart diagrams.", ScopeLive: true}}
	got, err := NewAnswerer(c).Answer(withMemory(rows...), "How?", AudienceBA, LanguageDE, twoSources(), Scope{}, "", func(string) {})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	block := strings.Index(*prompt, "Standing instructions from the reader")
	rule := strings.Index(*prompt, "- Never draw flowchart diagrams.")
	diagram := strings.LastIndex(*prompt, "```mermaid")
	closing := strings.LastIndex(*prompt, "German")
	if block < 0 || rule < block || block < diagram || closing < rule {
		t.Fatalf("block=%d rule=%d diagram=%d closing=%d:\n%s", block, rule, diagram, closing, *prompt)
	}
	if got.Memories != 1 {
		t.Fatalf("Memories = %d", got.Memories)
	}
}

func TestAnswer_withoutRulesThePromptIsWhatItWas(t *testing.T) {
	c, plain, _ := streamUpstream(t, "So [1].")
	if _, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", func(string) {}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	c, empty, _ := streamUpstream(t, "So [1].")
	if _, err := NewAnswerer(c).Answer(withMemory(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", func(string) {}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if *plain != *empty {
		t.Fatalf("an empty memory changed the prompt:\n%s\n---\n%s", *plain, *empty)
	}
	if strings.Contains(*plain, "Standing instructions") {
		t.Fatal("no rules, yet a block")
	}
}

func TestAnswer_aScopedRuleFollowsTheTurnsRepositories(t *testing.T) {
	// A rule scoped to a project of one: the store resolves members from
	// the index, and a row built here without them is a rule of a project
	// this turn is not about.
	c, prompt, _ := streamUpstream(t, "So [1].")
	rows := []memory.Row{{ID: 1, Text: "Skip the tests.", Scope: "shop", ScopeLive: true}}
	if _, err := NewAnswerer(c).Answer(withMemory(rows...), "How?", AudienceBA, LanguageEN, twoSources(), Scope{Known: []string{"peeq"}}, "", func(string) {}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Contains(*prompt, "Skip the tests") {
		t.Fatalf("a rule of another project reached the prompt:\n%s", *prompt)
	}
}

func TestRework_carriesTheReadersRulesToo(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "Short [1].")
	rows := []memory.Row{{ID: 1, Text: "Never draw flowchart diagrams.", ScopeLive: true}}
	th := Thread{Question: "How?", Answer: "Long [1].", Sources: twoSources(), SourcesTotal: 2}
	got, err := NewAnswerer(c).Rework(withMemory(rows...), "shorter", AudienceBA, LanguageEN, th, Scope{}, func(string) {})
	if err != nil {
		t.Fatalf("Rework: %v", err)
	}
	if !strings.Contains(*prompt, "- Never draw flowchart diagrams.") || got.Memories != 1 {
		t.Fatalf("Memories = %d, prompt:\n%s", got.Memories, *prompt)
	}
}

func TestMemoryAnswer_everyLanguageHasEveryLine(t *testing.T) {
	added := &memory.Added{
		Row:          memory.Row{ID: 1, Text: "Never draw flowchart diagrams.", Scope: "shop"},
		Replaced:     []string{"Always draw one."},
		Removed:      []string{"Keep it short."},
		ScopeDropped: "warehouse",
	}
	for _, lang := range []Language{LanguageEN, LanguageDE, LanguageFR, LanguageIT} {
		text := MemoryAnswer(lang, added, false)
		for _, want := range []string{`"Never draw flowchart diagrams"`, `"Always draw one"`, `"Keep it short"`, "shop", `"warehouse"`, "Memory"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: %q lacks %q", lang, text, want)
			}
		}
		if strings.Contains(text, "ß") {
			t.Errorf("%s: %q is not Swiss", lang, text)
		}
		if full := MemoryAnswer(lang, nil, true); !strings.Contains(full, "40") {
			t.Errorf("%s: full = %q", lang, full)
		}
		if nothing := MemoryAnswer(lang, &memory.Added{}, false); nothing == "" || strings.Contains(nothing, `""`) {
			t.Errorf("%s: nothing = %q", lang, nothing)
		}
	}
	// A removal alone says only what was forgotten.
	only := MemoryAnswer(LanguageEN, &memory.Added{Removed: []string{"Keep it short."}}, false)
	if only != `Forgotten: "Keep it short".` {
		t.Fatalf("only = %q", only)
	}
}
