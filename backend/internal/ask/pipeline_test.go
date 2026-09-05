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

// indexedSearch is searchFunc plus an index that carries some repositories.
// ResumeRepo asks whether the chosen one is still there before searching, so
// a fake that knows nothing would make every repository look deleted.
type indexedSearch struct {
	searchFunc
	indexed []string
}

func (s indexedSearch) ResolveRepos(_ context.Context, want []string, _ string) (known, unknown []string, err error) {
	for _, w := range want {
		for _, i := range s.indexed {
			if w == i {
				known = append(known, w)
			}
		}
	}
	return known, nil, nil
}

// withIndexedSearcher overrides the searcher with one whose index carries
// exactly the named repositories.
func withIndexedSearcher(indexed []string, fn func(retrieve.Query) ([]retrieve.Hit, error)) pipelineOpt {
	return func(f *pipelineFakes) { f.search = indexedSearch{searchFunc: searchFunc(fn), indexed: indexed} }
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

	got, _, err := p.Run(context.Background(), q, AudienceBA, LanguageEN, Thread{}, Events{})
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

	got, _, err := p.Run(context.Background(), "How does shipping work?", AudienceBA, LanguageEN, Thread{}, Events{})
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

	if _, _, err := p.Run(context.Background(), "How?", AudienceBA, LanguageEN, Thread{},
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
	answer, clar, err := p.Run(context.Background(), "frage", AudienceBA, LanguageEN, Thread{}, Events{})

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
	p := newTestPipeline(t, withIndexedSearcher([]string{"loom"}, func(q retrieve.Query) ([]retrieve.Hit, error) {
		got = q
		return []retrieve.Hit{{ChunkID: 1, Repo: "loom", Path: "a.go"}}, nil
	}))

	answer, err := p.ResumeRepo(context.Background(), "how are token costs calculated in $?",
		Understanding{CodeTerms: []string{"pricing"}}, []string{"loom"}, AudienceBA, LanguageEN, Scope{Known: []string{"loom"}}, Events{})
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

	if _, err := p.ResumeRepo(context.Background(), "frage", Understanding{}, nil,
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
	p := newTestPipeline(t, withIndexedSearcher([]string{"loom"}, func(retrieve.Query) ([]retrieve.Hit, error) {
		return nil, nil
	}))

	got, err := p.ResumeRepo(context.Background(), "frage", Understanding{CodeTerms: []string{"AirPlay"}}, []string{"loom"},
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

// TestResumeRepoRefusesWhenTheChosenRepositoryLeftTheIndex: a restriction the
// index cannot resolve is not a narrow search, it is no search at all —
// knownRepos drops a name it does not carry, and an empty restriction means
// the whole corpus. Between the card and the choice the repository can leave
// repos.yaml or be renamed. Answering from every repository while the record
// says it answered from the chosen one is the substitution this rung exists
// to prevent, so the turn fails instead and the card stays open for another
// choice.
func TestResumeRepoRefusesWhenTheChosenRepositoryLeftTheIndex(t *testing.T) {
	p := newTestPipeline(t, withIndexedSearcher([]string{"peeq"}, func(retrieve.Query) ([]retrieve.Hit, error) {
		t.Fatal("a repository the index no longer carries must not be searched for across the whole corpus")
		return nil, nil
	}))

	_, err := p.ResumeRepo(context.Background(), "frage", Understanding{}, []string{"loom"},
		AudienceBA, LanguageEN, Scope{Known: []string{"loom"}}, Events{})
	if err == nil {
		t.Fatal("want an error when the chosen repository is gone from the index")
	}
	if !strings.Contains(err.Error(), "loom") {
		t.Errorf("err = %v, want it to name the repository that went missing", err)
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

	got, _, err := p.Run(context.Background(), "in all repos, how are token costs calculated?", AudienceBA, LanguageEN, Thread{}, Events{})
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

// TestRunAnswersAFollowUpOutOfTheThreadsOwnRepository is the funnel. The
// follow-up names no repository — the reader named it a turn ago — and before
// the pin existed it was searched across the corpus and carded back with
// "which repository did you mean?", the one question the thread had already
// answered.
func TestRunAnswersAFollowUpOutOfTheThreadsOwnRepository(t *testing.T) {
	// Given a question that names nothing, in a thread narrowed to rongo.
	fr := &fakeRouter{}
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"loom", "rongo"}}
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[]}`, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), fr)

	// When the turn runs with the thread's pin.
	got, clar, err := p.Run(context.Background(), "Kannst du das in einem Diagramm aufzeigen?",
		AudienceBA, LanguageEN, Thread{Pin: []string{"rongo"}}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Then the search, the router and the record all say rongo.
	if clar != nil {
		t.Fatal("a thread that already narrowed must not be asked which repository was meant")
	}
	if len(search.got.Repos) != 1 || search.got.Repos[0] != "rongo" {
		t.Errorf("searched repos = %v, want the thread's own", search.got.Repos)
	}
	if len(fr.named) != 1 || fr.named[0] != "rongo" {
		t.Errorf("router saw named = %v, want the pin — the rung that would otherwise card reads this", fr.named)
	}
	if len(got.Scope.Known) != 1 || got.Scope.Known[0] != "rongo" {
		t.Errorf("scope = %+v, want the inherited repository in the record", got.Scope)
	}
}

// TestRunNeverWidensAPinnedThread: a thread narrows and never the other way.
// "In all repos" is a widening, so inside a pinned thread it is not honoured —
// the way out is a new thread, and the notice says so.
func TestRunNeverWidensAPinnedThread(t *testing.T) {
	fr := &fakeRouter{}
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"loom", "rongo"}}
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[],"all_repos":true}`, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), fr)

	got, _, err := p.Run(context.Background(), "in all repos, how are token costs calculated?",
		AudienceBA, LanguageEN, Thread{Pin: []string{"rongo"}}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fr.all || got.Scope.All {
		t.Error("a pinned thread must not answer across the corpus, whatever the question asks for")
	}
	if len(search.got.Repos) != 1 || search.got.Repos[0] != "rongo" {
		t.Errorf("searched repos = %v, want the pin to hold", search.got.Repos)
	}
}

// TestRunSaysWhichNamedRepositoryTheThreadLeftOut: the name was right and the
// code is indexed, and the turn still did not read it. An answer that dropped
// half of what was asked about silently reads exactly like one that covered it.
func TestRunSaysWhichNamedRepositoryTheThreadLeftOut(t *testing.T) {
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"loom", "rongo"}}
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":["loom"]}`, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	var notices []string
	got, _, err := p.Run(context.Background(), "und wie macht das loom?", AudienceBA, LanguageEN,
		Thread{Pin: []string{"rongo"}}, Events{OnNotice: func(text string) { notices = append(notices, text) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got.Scope.Outside) != 1 || got.Scope.Outside[0] != "loom" {
		t.Errorf("scope.Outside = %v, want the named repository the thread does not carry", got.Scope.Outside)
	}
	if len(got.Scope.Unknown) != 0 {
		t.Errorf("scope.Unknown = %v, want it empty — loom is indexed, it is just not this thread", got.Scope.Unknown)
	}
	joined := strings.Join(notices, " ")
	if !strings.Contains(joined, "loom") || !strings.Contains(joined, "new thread") {
		t.Errorf("notice = %q, want it to name loom and the way to ask about it", joined)
	}
	// And the narrowing is a fact, not just a sentence: knownRepos unions in
	// every indexed repository the QUESTION names as a whole word, so a pinned
	// search that carried the question would search loom after all — while the
	// notice above said it had not and the prompt said the turn knows nothing
	// about it.
	if search.got.Question != "" {
		t.Errorf("pinned search carried the question (%q), which unions the named repository back in", search.got.Question)
	}
	if len(search.got.Repos) != 1 || search.got.Repos[0] != "rongo" {
		t.Errorf("searched repos = %v, want the pin alone", search.got.Repos)
	}
}

// TestRunFailsWhenTheThreadsRepositoryLeftTheIndex: the pin comes off a stored
// row, and a repository can leave repos.yaml between two turns. knownRepos
// drops a name it does not carry and an empty restriction means the whole
// corpus, so the turn would answer from everything while the notice and the
// record both said one repository — the substitution ResumeRepo already fails
// rather than commits.
func TestRunFailsWhenTheThreadsRepositoryLeftTheIndex(t *testing.T) {
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"peeq"}}
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[]}`, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	_, _, err := p.Run(context.Background(), "und wie schnell ist das?", AudienceBA, LanguageEN,
		Thread{Pin: []string{"rongo"}}, Events{})
	if err == nil {
		t.Fatal("want an error when the thread's own repository is gone from the index")
	}
	if !strings.Contains(err.Error(), "rongo") {
		t.Errorf("err = %v, want it to name the repository that went missing", err)
	}
	if len(search.queries) != 0 {
		t.Errorf("ran %d searches, want none — an unresolvable pin must not become the whole corpus", len(search.queries))
	}
}

// TestRunSaysSoWhenAPinnedThreadCannotAnswerAcrossTheCorpus: refusing to widen
// is right, refusing silently is not. The question asked for every repository
// and names none, so there is nothing for Outside to carry — and without this
// the turn would hand the model a corpus-wide comparison question, one
// thread's sources, and no rule about the gap between them.
func TestRunSaysSoWhenAPinnedThreadCannotAnswerAcrossTheCorpus(t *testing.T) {
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"loom", "rongo"}}
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":[],"all_repos":true}`, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	var notices []string
	got, _, err := p.Run(context.Background(), "vergleiche das mit allen anderen Repositories", AudienceBA, LanguageEN,
		Thread{Pin: []string{"rongo"}}, Events{OnNotice: func(text string) { notices = append(notices, text) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.Scope.AllDenied {
		t.Error("the record never learned the reader asked for the whole corpus and did not get it")
	}
	if got.Scope.All {
		t.Error("a pinned thread must not record permission to answer across the corpus")
	}
	joined := strings.Join(notices, " ")
	if !strings.Contains(joined, "every repository") || !strings.Contains(joined, "new thread") {
		t.Errorf("notice = %q, want the reader told what they are not getting and how to get it", joined)
	}
}

// TestTheAnswerPromptForbidsInventingTheRestOfTheCorpus is the other half of
// that: the model holds a question asking about every repository and sources
// from one, which is answerMissingRepo's position with no name to put in it.
func TestTheAnswerPromptForbidsInventingTheRestOfTheCorpus(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	sc := Scope{Known: []string{"rongo"}, AllDenied: true}
	if _, err := NewAnswerer(c).Answer(context.Background(), "vergleiche das mit allen Repositories",
		AudienceBA, LanguageEN, twoSources(), sc, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !strings.Contains(*prompt, "Only rongo is in front of you") {
		t.Errorf("the prompt never says what the turn actually has:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "Make no claim of any kind about any other repository") {
		t.Errorf("nothing stops the model from writing the rest of the corpus out of training:\n%s", *prompt)
	}
}

// TestTheAnswerPromptForbidsClaimsAboutARepositoryTheThreadLeftOut: the model
// is handed "und wie macht das loom?" and rongo-only sources. Without a rule it
// writes loom's side out of its own training, which is the invention the whole
// prompt is built to prevent — the same position an unindexed repository puts
// it in, for a different reason.
func TestTheAnswerPromptForbidsClaimsAboutARepositoryTheThreadLeftOut(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	sc := Scope{Known: []string{"rongo"}, Outside: []string{"loom"}}
	if _, err := NewAnswerer(c).Answer(context.Background(), "und wie macht das loom?",
		AudienceBA, LanguageEN, twoSources(), sc, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !strings.Contains(*prompt, "this thread does not cover: loom") {
		t.Errorf("the prompt never names the repository the thread left out:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "make no claim of any kind about their code") {
		t.Errorf("the prompt never forbids inventing that repository's side:\n%s", *prompt)
	}
}

// TestTheAnswerPromptCarriesThePreviousQuestionAndNotItsAnswer holds the line
// this whole feature has to stay behind. The previous QUESTION is what a
// pronoun points at, so it goes in. The previous ANSWER is prose the model
// wrote itself, and handing it back alongside real sources is how a claim ends
// up carrying a citation it was never read from.
func TestTheAnswerPromptCarriesThePreviousQuestionAndNotItsAnswer(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(c).Answer(context.Background(), "Kannst du das in einem Diagramm aufzeigen?",
		AudienceBA, LanguageEN, twoSources(), Scope{},
		"Wie unterscheidet sich rongo von reinem RAG?", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !strings.Contains(*prompt, "Wie unterscheidet sich rongo von reinem RAG?") {
		t.Errorf("the previous question never reached the prompt:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "Do not restate what") {
		t.Errorf("nothing stops the turn from writing the same answer again:\n%s", *prompt)
	}
}

// TestTheAnswerPromptOfAFirstTurnSaysNothingAboutAFollowUp: the rule is added,
// never templated in empty.
func TestTheAnswerPromptOfAFirstTurnSaysNothingAboutAFollowUp(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(c).Answer(context.Background(), "How is pricing resolved?",
		AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Contains(*prompt, "This is a follow-up") {
		t.Errorf("a first turn must not be told it is following something up:\n%s", *prompt)
	}
}

// TestRunNarrowsFurtherInsideThePin: the pin is a ceiling, not a floor. A
// thread pinned to two repositories by a comparison is still a thread, and
// "and in rongo?" inside it has to reach rongo alone.
func TestRunNarrowsFurtherInsideThePin(t *testing.T) {
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"peeq", "rongo"}}
	c := twoStepUpstream(t, `{"intent":"how","terms":["t"],"code_terms":["c"],"repos":["rongo"]}`, "Answer.")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, _, err := p.Run(context.Background(), "and in rongo?", AudienceBA, LanguageEN,
		Thread{Pin: []string{"peeq", "rongo"}}, Events{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got.Scope.Known) != 1 || got.Scope.Known[0] != "rongo" {
		t.Errorf("scope = %+v, want the narrower half of the pin", got.Scope)
	}
	if len(got.Scope.Outside) != 0 {
		t.Errorf("scope.Outside = %v, want nothing — rongo is inside the pin", got.Scope.Outside)
	}
	if len(search.got.Repos) != 1 || search.got.Repos[0] != "rongo" {
		t.Errorf("searched repos = %v, want only the narrowed one", search.got.Repos)
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

// TestResumeRepoSearchesEveryChosenRepositorySeparately is the too-broad
// panel's own resume: the reader picked more than one repository off it, and
// two or more named repositories are a comparison, which searches each side at
// full depth rather than letting one of them fill a single fused cut.
func TestResumeRepoSearchesEveryChosenRepositorySeparately(t *testing.T) {
	var queries []retrieve.Query
	p := newTestPipeline(t, withIndexedSearcher([]string{"loom", "peeq"}, func(q retrieve.Query) ([]retrieve.Hit, error) {
		queries = append(queries, q)
		return []retrieve.Hit{{ChunkID: 1, Repo: q.Repos[0], Path: "a.go"}}, nil
	}))

	answer, err := p.ResumeRepo(context.Background(), "how is retry done?",
		Understanding{CodeTerms: []string{"retry"}}, []string{"loom", "peeq"},
		AudienceBA, LanguageEN, Scope{Known: []string{"loom", "peeq"}}, Events{})
	if err != nil {
		t.Fatalf("resume repo: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("ran %d searches, want one per chosen repository", len(queries))
	}
	for i, q := range queries {
		if len(q.Repos) != 1 {
			t.Errorf("search %d covered %v, want one repository at a time", i, q.Repos)
		}
		if q.K != searchK {
			t.Errorf("search %d used K = %d, want each side at full depth (%d)", i, q.K, searchK)
		}
		if q.Question != "" {
			t.Errorf("search %d kept the question (%q); knownRepos would union the others back in", i, q.Question)
		}
	}
	if answer.Text == "" {
		t.Error("want an answer")
	}
}

// The router's "too broad" has to reach the record, or a reload renders the
// panel as a card and offers twenty buttons where the turn asked for a
// narrower question.
func TestRunCarriesTooBroadIntoTheClarification(t *testing.T) {
	p := newTestPipeline(t, func(f *pipelineFakes) {
		f.router = &fakeRouter{d: Decision{Ask: true, TooBroad: true, Candidates: []Candidate{
			{Repo: "peeq", Branch: "master"},
			{Repo: "loom", Branch: "main"},
		}}}
	})

	_, clar, err := p.Run(context.Background(), "frage", AudienceBA, LanguageEN, Thread{}, Events{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if clar == nil {
		t.Fatal("want a clarification")
	}
	if !clar.TooBroad {
		t.Error("the turn asked for a narrower question; the record must say so")
	}
}
