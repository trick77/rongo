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
	// queries is every query the turn ran, in order. A comparison turn runs
	// one per named repository, and only the sequence shows that.
	queries []retrieve.Query
	// indexed is what this fake index carries. Nil means "whatever the
	// question named", which is what every test that does not care about
	// scope wants: the guess passes through unchanged, as it did before
	// ResolveRepos existed.
	indexed []string
}

func (f *fakeSearch) Search(_ context.Context, q retrieve.Query) ([]retrieve.Hit, error) {
	f.got = q
	f.queries = append(f.queries, q)
	return f.hits, nil
}

// ResolveRepos answers from indexed: the names it holds are the ones this
// fake index carries, and everything else the question named is unknown.
func (f *fakeSearch) ResolveRepos(_ context.Context, want []string, _ string) (known, unknown []string, err error) {
	if f.indexed == nil {
		return want, nil, nil
	}
	for _, n := range want {
		found := false
		for _, have := range f.indexed {
			if have == n {
				found = true
				break
			}
		}
		if found {
			known = append(known, n)
		} else {
			unknown = append(unknown, n)
		}
	}
	return known, unknown, nil
}

// searchFunc adapts a plain function to Searcher, for tests that only care
// whether search was called at all — Resume and Reexplain must never call it.
type searchFunc func(retrieve.Query) ([]retrieve.Hit, error)

func (f searchFunc) Search(_ context.Context, q retrieve.Query) ([]retrieve.Hit, error) {
	return f(q)
}

// ResolveRepos knows no repository: these tests never name one, and a fake
// that invented an index would hide a turn that searched the wrong scope.
func (f searchFunc) ResolveRepos(_ context.Context, _ []string, _ string) (known, unknown []string, err error) {
	return nil, nil, nil
}

// fakeRouter returns a canned Decision regardless of input, so pipeline tests
// can drive Run's ask/don't-ask branch without a database, a margin or a
// model — Router's own ladder is route_test.go's job.
type fakeRouter struct {
	d   Decision
	err error
	// named is what Run passed as the question's resolved repositories, so a
	// test can check the rung's input reaches the router at all.
	named []string
	// all is the understanding's "the reader asked for every repository"
	// signal, for the same reason.
	all bool
}

func (f *fakeRouter) Route(_ context.Context, _ string, _ Audience, _ Language, _ []retrieve.Hit, namedRepos []string, allRepos bool) (Decision, error) {
	f.named = namedRepos
	f.all = allRepos
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
			Title:     fmt.Sprintf("Candidate %d", i),
			Summary:   "Testkandidat",
			Hits:      []retrieve.Hit{{ChunkID: int64(i + 1), Repo: repo, Path: "a.go"}},
		}
	}
	return func(f *pipelineFakes) { f.router = &fakeRouter{d: Decision{Ask: true, Candidates: cs}} }
}

// newTestPipeline builds a Pipeline over a migrated database, an upstream
// that answers both the understanding call and the final answer, a searcher
// that returns nothing, and a router that never asks — so each test overrides
// only the piece it is testing.
func newTestPipeline(t *testing.T, opts ...pipelineOpt) *Pipeline {
	t.Helper()
	f := pipelineFakes{
		search: &fakeSearch{},
		router: &fakeRouter{},
	}
	for _, o := range opts {
		o(&f)
	}
	db := gatherDB(t)
	c := twoStepUpstream(t, appleTVReply, "Answer [1].")
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
	c := twoStepUpstream(t, appleTVReply, "Access runs through a grant [1].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	q := "How does an Apple TV get at the media file without signing in?"

	got, _, err := p.Run(context.Background(), q, AudienceBA, LanguageEN, Events{})
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
	c := twoStepUpstream(t, appleTVReply, "I suspect ...")
	p := NewPipeline(c, &fakeSearch{}, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, _, err := p.Run(context.Background(), "How does shipping work?", AudienceBA, LanguageEN, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(got.Text, "found nothing") {
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
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})
	var steps []string

	if _, _, err := p.Run(context.Background(), "How?", AudienceBA, LanguageEN,
		Events{OnStatus: func(s string) { steps = append(steps, s) }}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// "writing" follows "answering" on the first token: the reader is told the
	// model is thinking until there is text, and only then that it is writing.
	want := []string{"understanding", "searching", "routing", "gathering", "answering", "writing"}
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
	answer, clar, err := p.Run(context.Background(), "frage", AudienceBA, LanguageEN, Events{})

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
	answer, err := p.Resume(context.Background(), "frage", AudienceBA, LanguageEN,
		[]retrieve.Hit{{ChunkID: 1, Repo: "peeq", Path: "a.go"}}, Scope{}, Events{})

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

// TestResumeRepoSearchesTheChosenRepositoryAtFullDepth is the one resume path
// that searches again. A repository card groups hits out of ONE fused list of
// searchK, so the runner-up repository may have contributed two or three
// chunks; answering from those would make the choice worse than the answer it
// replaced. Searching the chosen repository on its own gives it the depth a
// question naming it would have got.
func TestResumeRepoSearchesTheChosenRepositoryAtFullDepth(t *testing.T) {
	var got retrieve.Query
	p := newTestPipeline(t, withSearcher(func(q retrieve.Query) ([]retrieve.Hit, error) {
		got = q
		return []retrieve.Hit{{ChunkID: 1, Repo: "loom", Path: "a.go"}}, nil
	}))

	answer, err := p.ResumeRepo(context.Background(), "how are token costs calculated in $?",
		Understanding{CodeTerms: []string{"pricing"}}, "loom", AudienceBA, LanguageEN, Scope{Known: []string{"loom"}}, Events{})
	if err != nil {
		t.Fatalf("resume repo: %v", err)
	}
	if len(got.Repos) != 1 || got.Repos[0] != "loom" {
		t.Errorf("searched repos = %v, want only the chosen one", got.Repos)
	}
	if got.K != searchK {
		t.Errorf("searched K = %d, want the same depth a named repository gets (%d)", got.K, searchK)
	}
	if got.Question != "" {
		t.Errorf("Question = %q, want it left out — knownRepos would union the other repositories back in", got.Question)
	}
	if got.Texts[0] != "how are token costs calculated in $?" {
		t.Errorf("texts = %v, want the stored understanding's search texts, question first", got.Texts)
	}
	if answer.Text == "" {
		t.Error("want an answer")
	}
	if answer.Scope.Known[0] != "loom" {
		t.Errorf("scope = %+v, want the choice carried into the record", answer.Scope)
	}
}

// TestResumeRepoWithNoRepositorySearchesTheWholeCorpus is the card's "all
// repositories" entry: the search the turn would have run had it never asked.
func TestResumeRepoWithNoRepositorySearchesTheWholeCorpus(t *testing.T) {
	var got retrieve.Query
	p := newTestPipeline(t, withSearcher(func(q retrieve.Query) ([]retrieve.Hit, error) {
		got = q
		return []retrieve.Hit{{ChunkID: 1, Repo: "peeq", Path: "a.go"}}, nil
	}))

	if _, err := p.ResumeRepo(context.Background(), "frage", Understanding{}, "",
		AudienceBA, LanguageEN, Scope{All: true}, Events{}); err != nil {
		t.Fatalf("resume repo: %v", err)
	}
	if len(got.Repos) != 0 {
		t.Errorf("searched repos = %v, want the whole corpus", got.Repos)
	}
	if got.Question == "" {
		t.Error("the unscoped search keeps the question, the way an ordinary turn does")
	}
}

// TestResumeRepoSaysNothingFoundRatherThanAnswering: a repository can be
// chosen and turn out to hold nothing for this question. "No hit means no
// hit" — never an answer assembled from what the card happened to show.
func TestResumeRepoSaysNothingFoundRatherThanAnswering(t *testing.T) {
	p := newTestPipeline(t, withSearcher(func(retrieve.Query) ([]retrieve.Hit, error) {
		return nil, nil
	}))

	got, err := p.ResumeRepo(context.Background(), "frage", Understanding{CodeTerms: []string{"AirPlay"}}, "loom",
		AudienceBA, LanguageEN, Scope{Known: []string{"loom"}}, Events{})
	if err != nil {
		t.Fatalf("resume repo: %v", err)
	}
	if !strings.Contains(got.Text, "found nothing") {
		t.Errorf("text = %q, want the nothing-found answer", got.Text)
	}
	if !strings.Contains(strings.ToLower(got.Text), "airplay") {
		t.Errorf("text = %q, want the terms it tried named", got.Text)
	}
}

// TestRunCarriesTheAllReposSignalToTheRouterAndTheRecord: the reader's own
// words have to reach the rung that would otherwise ask them again, and the
// stored scope, so a re-explain answers under the same permission.
func TestRunCarriesTheAllReposSignalToTheRouterAndTheRecord(t *testing.T) {
	fr := &fakeRouter{}
	db := gatherDB(t)
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[],"all_repos":true}`, "Answer.")
	p := NewPipeline(c, &fakeSearch{}, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), fr)

	got, _, err := p.Run(context.Background(), "in all repos, how are token costs calculated?", AudienceBA, LanguageEN, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !fr.all {
		t.Error("the router never learned the reader asked for every repository")
	}
	if !got.Scope.All {
		t.Error("the record never learned it either, so a re-explain would ask which repository was meant")
	}
}

func TestReexplainAnswersFromStoredSourcesWithoutSearchingOrGathering(t *testing.T) {
	p := newTestPipeline(t, withSearcher(func(retrieve.Query) ([]retrieve.Hit, error) {
		t.Fatal("re-explaining must not search")
		return nil, nil
	}))

	answer, err := p.Reexplain(context.Background(), "frage", AudienceDev, LanguageEN,
		[]Source{{ChunkID: 1, Repo: "peeq", Path: "a.go", Text: "package a", StartLine: 1, EndLine: 1}}, Scope{}, Events{})
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
	_, err := p.Reexplain(context.Background(), "frage", AudienceDev, LanguageEN, nil, Scope{}, Events{})
	if err == nil {
		t.Fatal("want an error when the gathered basis is gone")
	}
}
