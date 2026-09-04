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
	de := ScopeNotice(LanguageDE, []string{"rongo"}, []string{"loom"})
	if !strings.Contains(de, "Kein Repository") {
		t.Errorf("German notice = %q", de)
	}
	if strings.Contains(de, "ß") {
		t.Errorf("German notice = %q, want Swiss orthography", de)
	}
	// An unknown language falls back to English rather than to an empty
	// string: a missing notice is worse than an English one.
	if got := ScopeNotice(Language("xx"), []string{"rongo"}, []string{"loom"}); !strings.Contains(got, "No repository") {
		t.Errorf("fallback notice = %q", got)
	}
	// Nothing missing, nothing said.
	if got := ScopeNotice(LanguageEN, []string{"rongo"}, nil); got != "" {
		t.Errorf("notice = %q, want none when every named repository was found", got)
	}
	// Nothing named that the index knows: the turn searched everything, and
	// saying "answered for  alone" would name nothing at all.
	if got := ScopeNotice(LanguageEN, nil, []string{"loom"}); !strings.Contains(got, "all indexed repositories") {
		t.Errorf("notice = %q, want it to say the whole corpus was searched", got)
	}
}

func TestAnswerPromptCarriesTheComparisonAndTheMissingRepository(t *testing.T) {
	// Never invent: without the missing-repository rule the model is handed
	// "how do loom and rongo differ" plus rongo-only sources and writes
	// loom's side out of its training.
	c, prompt, _ := streamUpstream(t, "x")
	_, err := NewAnswerer(c).Answer(context.Background(), "How do peeq and rongo differ?", AudienceBA, LanguageEN,
		twoSources(), Scope{Known: []string{"peeq", "rongo"}, Unknown: []string{"loom"}}, nil)
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
