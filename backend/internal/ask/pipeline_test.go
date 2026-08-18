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
	"github.com/trick77/rongo/internal/retrieve"
)

// fakeSearch records the query it was handed and returns fixed hits.
type fakeSearch struct {
	hits []retrieve.Hit
	got  retrieve.Query
}

func (f *fakeSearch) Search(_ context.Context, q retrieve.Query) ([]retrieve.Hit, error) {
	f.got = q
	return f.hits, nil
}

// searchFunc adapts a plain function to Searcher, for tests that only care
// whether search was called at all — Resume and Reexplain must never call it.
type searchFunc func(retrieve.Query) ([]retrieve.Hit, error)

func (f searchFunc) Search(_ context.Context, q retrieve.Query) ([]retrieve.Hit, error) {
	return f(q)
}

// fakeRouter returns a canned Decision regardless of input, so pipeline tests
// can drive Run's ask/don't-ask branch without a database, a margin or a
// model — Router's own ladder is route_test.go's job.
type fakeRouter struct {
	d   Decision
	err error
}

func (f fakeRouter) Route(_ context.Context, _ string, _ []retrieve.Hit) (Decision, error) {
	return f.d, f.err
}

// pipelineFakes is what newTestPipeline wires by default; an option overrides
// one piece without a test having to restate the rest.
type pipelineFakes struct {
	search Searcher
	router Routes
}

type pipelineOpt func(*pipelineFakes)

// withSearcher overrides the searcher a test pipeline is built with.
func withSearcher(fn func(retrieve.Query) ([]retrieve.Hit, error)) pipelineOpt {
	return func(f *pipelineFakes) { f.search = searchFunc(fn) }
}

// withRouterAsking makes the router end the turn with n named candidates,
// each carrying its own single hit so a later Resume can be driven from it.
func withRouterAsking(n int) pipelineOpt {
	cs := make([]Candidate, n)
	for i := range cs {
		repo := fmt.Sprintf("repo%d", i)
		cs[i] = Candidate{
			Repo:      repo,
			ModuleKey: fmt.Sprintf("m%d", i),
			Title:     fmt.Sprintf("Kandidat %d", i),
			Summary:   "Testkandidat",
			Hits:      []retrieve.Hit{{ChunkID: int64(i + 1), Repo: repo, Path: "a.go"}},
		}
	}
	return func(f *pipelineFakes) { f.router = fakeRouter{d: Decision{Ask: true, Candidates: cs}} }
}

// newTestPipeline builds a Pipeline over a migrated database, an upstream
// that answers both the understanding call and the final answer, a searcher
// that returns nothing, and a router that never asks — so each test overrides
// only the piece it is testing.
func newTestPipeline(t *testing.T, opts ...pipelineOpt) *Pipeline {
	t.Helper()
	f := pipelineFakes{
		search: &fakeSearch{},
		router: fakeRouter{},
	}
	for _, o := range opts {
		o(&f)
	}
	db := gatherDB(t)
	c := twoStepUpstream(t, appleTVReply, "Antwort [1].")
	return NewPipeline(c, f.search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), f.router)
}

// twoStepUpstream answers the understanding call with JSON and the answer call
// with a stream, telling them apart by whether streaming was requested.
func twoStepUpstream(t *testing.T, understanding string, answerTokens ...string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": understanding}}},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := http.NewResponseController(w)
		for _, tok := range answerTokens {
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
	return llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client())
}

func TestPipeline_searchesWithTheExpansionNotJustTheQuestion(t *testing.T) {
	// The measured point of this phase. Searching the raw question alone is
	// what left the six questions at ranks 23 to 52.
	db := gatherDB(t)
	hitID := seedChunk(t, db, "backend/internal/playbackgrant/store.go", 0, 1, 20, "NewGrant", "func NewGrant() {}")
	search := &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}}
	c := twoStepUpstream(t, appleTVReply, "Der Zugriff laeuft ueber einen Grant [1].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), fakeRouter{})
	q := "Wie kommt ein Apple TV ohne Anmeldung an die Mediendatei?"

	got, _, err := p.Run(context.Background(), q, AudienceBA, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(search.got.Texts) < 2 {
		t.Fatalf("search texts = %v, want the question plus expansions", search.got.Texts)
	}
	if !strings.Contains(strings.ToLower(strings.Join(search.got.Texts, " ")), "airplay") {
		t.Errorf("search texts = %v, want the guessed code vocabulary", search.got.Texts)
	}
	if len(search.got.Repos) != 1 || search.got.Repos[0] != "peeq" {
		t.Errorf("repos = %v, want the hint from the understanding step", search.got.Repos)
	}
	if len(got.Citations) != 1 {
		t.Errorf("citations = %+v", got.Citations)
	}
}

func TestPipeline_nothingFoundNamesTheTermsItTried(t *testing.T) {
	// A dead end someone can act on — the vocabulary was wrong, ask
	// differently — instead of a shrug. And never an answer assembled from
	// whatever happened to be in context.
	db := gatherDB(t)
	c := twoStepUpstream(t, appleTVReply, "Ich vermute ...")
	p := NewPipeline(c, &fakeSearch{}, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), fakeRouter{})

	got, _, err := p.Run(context.Background(), "Wie laeuft der Versand?", AudienceBA, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(got.Text, "nichts gefunden") {
		t.Errorf("text = %q", got.Text)
	}
	if !strings.Contains(strings.ToLower(got.Text), "airplay") {
		t.Errorf("text = %q, want the tried terms named", got.Text)
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %+v, want none", got.Citations)
	}
}

func TestPipeline_reportsEveryStepInOrder(t *testing.T) {
	db := gatherDB(t)
	hitID := seedChunk(t, db, "a.go", 0, 1, 10, "f", "func f() {}")
	c := twoStepUpstream(t, appleTVReply, "So [1].")
	p := NewPipeline(c, &fakeSearch{hits: []retrieve.Hit{hitFor(t, db, hitID)}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), fakeRouter{})
	var steps []string

	if _, _, err := p.Run(context.Background(), "Wie?", AudienceBA,
		Events{OnStatus: func(s string) { steps = append(steps, s) }}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"verstehen", "suchen", "routen", "sammeln", "antworten"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("steps = %v, want %v", steps, want)
		}
	}
}

func TestRunEndsTheTurnWithAClarification(t *testing.T) {
	// Given a router that asks
	p := newTestPipeline(t, withRouterAsking(2))

	// When
	answer, clar, err := p.Run(context.Background(), "frage", AudienceBA, Events{})

	// Then
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if clar == nil {
		t.Fatal("want a clarification")
	}
	if answer.Text != "" {
		t.Errorf("a turn that asks writes no answer, got %q", answer.Text)
	}
	if len(clar.Candidates) != 2 {
		t.Errorf("got %d candidates", len(clar.Candidates))
	}
	if len(clar.Understanding.CodeTerms) == 0 {
		t.Error("the understanding travels with the clarification, or the resumed turn re-derives different terms")
	}
}

func TestResumeGathersFromTheGivenHitsAndNeverSearches(t *testing.T) {
	// Given a searcher that fails the test if it is called
	p := newTestPipeline(t, withSearcher(func(retrieve.Query) ([]retrieve.Hit, error) {
		t.Fatal("a resumed turn must not search again: the answer has to come from what the card offered")
		return nil, nil
	}))

	// When
	answer, err := p.Resume(context.Background(), "frage", AudienceBA,
		[]retrieve.Hit{{ChunkID: 1, Repo: "peeq", Path: "a.go"}}, Events{})

	// Then
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if answer.Text == "" {
		t.Error("want an answer")
	}
	if len(answer.Sources) == 0 {
		t.Error("the answer must carry what it was written from, so the turn can be re-explained later")
	}
}

func TestReexplainAnswersFromStoredSourcesWithoutSearchingOrGathering(t *testing.T) {
	p := newTestPipeline(t, withSearcher(func(retrieve.Query) ([]retrieve.Hit, error) {
		t.Fatal("re-explaining must not search")
		return nil, nil
	}))

	answer, err := p.Reexplain(context.Background(), "frage", AudienceDev,
		[]Source{{ChunkID: 1, Repo: "peeq", Path: "a.go", Text: "package a", StartLine: 1, EndLine: 1}}, Events{})
	if err != nil {
		t.Fatalf("reexplain: %v", err)
	}
	if answer.Text == "" {
		t.Error("want an answer")
	}
}

func TestReexplainRefusesWhenNothingIsLeftToAnswerFrom(t *testing.T) {
	// A re-index can remove a chunk. Answering the same question from
	// different code would be a silent substitution — exactly what the
	// invariants forbid.
	p := newTestPipeline(t)
	_, err := p.Reexplain(context.Background(), "frage", AudienceDev, nil, Events{})
	if err == nil {
		t.Fatal("want an error when the gathered basis is gone")
	}
}
