package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// twoReposReply is an understanding naming two repositories, as the model
// returns it for "how do peeq and rongo differ in X".
const twoReposReply = `{"intent":"how","terms":["session handling"],"code_terms":["Session"],"repos":["peeq","rongo"]}`

// oneMissingReply names a repository the index does not carry alongside one it
// does — the loom case.
const oneMissingReply = `{"intent":"how","terms":["session handling"],"code_terms":["Session"],"repos":["loom","rongo"]}`

// threeReposReply names more repositories than the cap allows at full depth.
const threeReposReply = `{"intent":"how","terms":["x"],"code_terms":["X"],"repos":["peeq","rongo","go-sqlite3"]}`

// bothReposSources is one source from each of the two repositories a
// comparison question names.
func bothReposSources() []Source {
	return []Source{
		{ChunkID: 1, Repo: "peeq", Branch: "master", Path: "backend/internal/sched/sched.go",
			StartLine: 1, EndLine: 30, Text: "func Sleep() {}", Reason: "hit"},
		{ChunkID: 2, Repo: "rongo", Branch: "master", Path: "backend/internal/sched/sched.go",
			StartLine: 1, EndLine: 30, Text: "func Jittered() {}", Reason: "hit"},
	}
}

func TestAnswerDoesNotPromiseToCoverARepositoryWithNoSources(t *testing.T) {
	// A named repository can be indexed, be searched on its own and still
	// return nothing for this question. "Cover every one of them" would then
	// be an instruction to invent — the one thing the rest of the prompt is
	// built to prevent.
	c, prompt, _ := streamUpstream(t, "x")
	_, err := NewAnswerer(c).Answer(context.Background(), "How do peeq and rongo differ?", AudienceBA, LanguageEN,
		twoSources(), Scope{Known: []string{"peeq", "rongo"}}, nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// twoSources is peeq only: one covered repository is not a comparison.
	if strings.Contains(*prompt, "The question names these repositories") {
		t.Errorf("the comparison rule must rest on the sources, not on the names:\n%s", *prompt)
	}
}

func TestDecideDoesNotAskWhenTheQuestionNamedBothRepositories(t *testing.T) {
	// Given two candidates too close for the margin, which every later rung
	// would turn into a card.
	tight := []Candidate{{Score: 1.0}, {Score: 0.99}}

	// When the question named two indexed repositories.
	// Then no rung below can reach: the reader asked about both, and a card
	// would ask them to answer a question they already answered.
	if Decide(tight, 0.25, false, true, 2) {
		t.Error("a question naming two repositories must be answered, not asked about")
	}
	// One named repository is not a comparison, and changes nothing.
	if !Decide(tight, 0.25, false, true, 1) {
		t.Error("one named repository must leave the ladder alone")
	}
	if !Decide(tight, 0.25, false, true, 0) {
		t.Error("no named repository must leave the ladder alone")
	}
}

func TestRouteSkipsTheJudgeWhenTwoRepositoriesWereNamed(t *testing.T) {
	// Given a router whose model must not be called at all.
	db := testDBWithDeps(t, nil)
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		t.Fatalf("naming both repositories settles it; no model may be asked. prompt: %q", prompt)
		return ""
	}), db)

	// When two independent candidates sit at the same score, and the question
	// named both repositories.
	got, err := r.Route(context.Background(), "how do peeq and rongo differ?", LanguageEN, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "rongo", Path: "backend/internal/auth/session.go", Score: 0.49},
	}, []string{"peeq", "rongo"})

	// Then
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("a question naming both repositories must not produce a card")
	}
	if len(got.Candidates) != 2 {
		t.Errorf("both sides must stay in the turn, got %d", len(got.Candidates))
	}
}

func TestPipelineSearchesEachNamedRepositoryOnItsOwn(t *testing.T) {
	// Given a corpus carrying both named repositories.
	// One fused cut of 20 can be filled entirely by one repository, and then
	// the "comparison" has one side. Searching each named repository
	// separately is what makes both sides a fact.
	db := gatherDB(t)
	search := &fakeSearch{indexed: []string{"peeq", "rongo"}}
	c := twoStepUpstream(t, twoReposReply, "Both keep a session [1].")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	// When
	if _, _, err := p.Run(context.Background(), "How do peeq and rongo differ in session handling?",
		AudienceBA, LanguageEN, Events{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Then one query per named repository, each restricted to that one.
	if len(search.queries) != 2 {
		t.Fatalf("ran %d searches, want one per named repository", len(search.queries))
	}
	for i, want := range []string{"peeq", "rongo"} {
		if len(search.queries[i].Repos) != 1 || search.queries[i].Repos[0] != want {
			t.Errorf("search %d restricted to %v, want only %q", i, search.queries[i].Repos, want)
		}
	}
}

func TestPipelineCapsWhatAComparisonCarriesOutOfRetrieval(t *testing.T) {
	// Gather never evicts a search hit, so every hit here becomes a source in
	// the answer prompt. Four named repositories at full depth would inline
	// eighty chunks of raw code with nothing to stop them.
	db := gatherDB(t)
	hits := make([]retrieve.Hit, searchK)
	for i := range hits {
		hits[i] = retrieve.Hit{ChunkID: int64(i + 1), Repo: "peeq", Path: "a.go", Score: float64(searchK - i)}
	}
	search := &fakeSearch{hits: hits, indexed: []string{"peeq", "rongo", "go-sqlite3"}}
	c := twoStepUpstream(t, threeReposReply, "x")
	p := NewPipeline(c, search, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, err := p.searchScoped(context.Background(), "q", []string{"q"}, []string{"peeq", "rongo", "go-sqlite3"})
	if err != nil {
		t.Fatalf("searchScoped: %v", err)
	}

	if len(got) != comparisonK {
		t.Errorf("carried %d hits out of three repositories, want the cap of %d", len(got), comparisonK)
	}
	// Best first, so the cap keeps the strongest evidence rather than
	// whichever repository happened to be searched first.
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Fatalf("hits are not ordered best first at %d", i)
		}
	}
	_ = got
}

func TestPipelineTellsTheRouterWhichRepositoriesWereNamed(t *testing.T) {
	// The rung is Decide's, and Decide only sees what Route is handed.
	db := gatherDB(t)
	router := &fakeRouter{}
	c := twoStepUpstream(t, twoReposReply, "x")
	p := NewPipeline(c, &fakeSearch{indexed: []string{"peeq", "rongo"}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), router)

	if _, _, err := p.Run(context.Background(), "How do peeq and rongo differ?",
		AudienceBA, LanguageEN, Events{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(router.named) != 2 {
		t.Errorf("router was told %v, want both named repositories", router.named)
	}
}

func TestPipelineSaysWhenANamedRepositoryIsNotIndexed(t *testing.T) {
	// The search drops an unknown name on purpose — a mishearing must not
	// wipe the result — so this notice is the only thing standing between
	// the reader and an answer about code they did not ask about.
	db := gatherDB(t)
	var notices []string
	c := twoStepUpstream(t, oneMissingReply, "rongo keeps no session [1].")
	p := NewPipeline(c, &fakeSearch{indexed: []string{"peeq", "rongo"}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, _, err := p.Run(context.Background(), "How do loom and rongo differ?", AudienceBA, LanguageEN,
		Events{OnNotice: func(text string) { notices = append(notices, text) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(notices) != 1 {
		t.Fatalf("notices = %v, want exactly one", notices)
	}
	if !strings.Contains(notices[0], "loom") {
		t.Errorf("notice = %q, want it to name the missing repository", notices[0])
	}
	if !strings.Contains(notices[0], "rongo") {
		t.Errorf("notice = %q, want it to name what was answered instead", notices[0])
	}
	if len(got.Scope.Unknown) != 1 || got.Scope.Unknown[0] != "loom" {
		t.Errorf("scope.Unknown = %v, want the missing repository carried out of the turn", got.Scope.Unknown)
	}
	if len(got.Scope.Known) != 1 || got.Scope.Known[0] != "rongo" {
		t.Errorf("scope.Known = %v, want the indexed one", got.Scope.Known)
	}
}

func TestPipelineSaysNothingWhenEveryNamedRepositoryIsIndexed(t *testing.T) {
	// The ordinary turn. A notice on every question would be noise, and the
	// one that matters would stop being read.
	db := gatherDB(t)
	var notices []string
	c := twoStepUpstream(t, twoReposReply, "x")
	p := NewPipeline(c, &fakeSearch{indexed: []string{"peeq", "rongo"}},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	if _, _, err := p.Run(context.Background(), "How do peeq and rongo differ?", AudienceBA, LanguageEN,
		Events{OnNotice: func(text string) { notices = append(notices, text) }}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(notices) != 0 {
		t.Errorf("notices = %v, want none", notices)
	}
}

func TestScopeNoticeFollowsTheAnswerLanguage(t *testing.T) {
	// Everything a person reads follows ask.Language. The notice is read by a
	// person, and it is templated rather than written by a model.
	de := ScopeNotice(LanguageDE, Scope{Known: []string{"rongo"}, Unknown: []string{"loom"}})
	if !strings.Contains(de, "Kein Repository") {
		t.Errorf("German notice = %q", de)
	}
	if strings.Contains(de, "ß") {
		t.Errorf("German notice = %q, want Swiss orthography", de)
	}
	// An unknown language falls back to English rather than to an empty
	// string: a missing notice is worse than an English one.
	if got := ScopeNotice(Language("xx"), Scope{Known: []string{"rongo"}, Unknown: []string{"loom"}}); !strings.Contains(got, "No repository") {
		t.Errorf("fallback notice = %q", got)
	}
	// Nothing missing, nothing said.
	if got := ScopeNotice(LanguageEN, Scope{Known: []string{"rongo"}}); got != "" {
		t.Errorf("notice = %q, want none when every named repository was found", got)
	}
	// Nothing named that the index knows: the turn searched everything, and
	// saying "answered for  alone" would name nothing at all.
	if got := ScopeNotice(LanguageEN, Scope{Unknown: []string{"loom"}}); !strings.Contains(got, "all indexed repositories") {
		t.Errorf("notice = %q, want it to say the whole corpus was searched", got)
	}
}

func TestAnswerPromptCarriesTheComparisonAndTheMissingRepository(t *testing.T) {
	// Never invent: without the missing-repository rule the model is handed
	// "how do loom and rongo differ" plus rongo-only sources and writes
	// loom's side out of its training.
	c, prompt, _ := streamUpstream(t, "x")
	_, err := NewAnswerer(c).Answer(context.Background(), "How do peeq and rongo differ?", AudienceBA, LanguageEN,
		bothReposSources(), Scope{Known: []string{"peeq", "rongo"}, Unknown: []string{"loom"}}, nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if !strings.Contains(*prompt, "peeq, rongo") {
		t.Errorf("prompt does not name the compared repositories:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "NOT indexed") {
		t.Errorf("prompt does not rule out the missing repository:\n%s", *prompt)
	}
	// The language pin stays last, whatever was inserted before it.
	langAt := strings.LastIndex(*prompt, "Language: every sentence")
	if langAt < strings.LastIndex(*prompt, "NOT indexed") {
		t.Error("the language rule must stay the last thing in the system prompt")
	}
}

func TestAnswerPromptStaysUnchangedForAnOrdinaryTurn(t *testing.T) {
	// One named repository is not a comparison, and nothing is missing: the
	// prompt must be what it was before any of this existed.
	c, prompt, _ := streamUpstream(t, "x")
	_, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN,
		twoSources(), Scope{Known: []string{"peeq"}}, nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if strings.Contains(*prompt, "The question names these repositories") {
		t.Errorf("a single-repository question must not get the comparison rule:\n%s", *prompt)
	}
	if strings.Contains(*prompt, "NOT indexed") {
		t.Errorf("nothing was missing; no rule about it belongs in the prompt:\n%s", *prompt)
	}
}
