package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// docOnlyHits is a search result that found documentation and nothing else —
// the ordinary shape when a natural-language question matches prose before it
// matches identifiers.
func docOnlyHits() []retrieve.Hit {
	return []retrieve.Hit{
		{ChunkID: 1, Repo: "peeq", Path: "AGENTS.md", RawText: "Two MiMo deployments, hardcoded."},
		{ChunkID: 2, Repo: "peeq", Path: "docs/models.md", RawText: "Pro where a human reads."},
	}
}

// docSources is what a turn gathers when a natural-language question matched
// the prose and nothing else: two documents, no code.
func docSources() []Source {
	return []Source{
		{ChunkID: 1, Repo: "rongo", Branch: "master", Path: "AGENTS.md",
			StartLine: 1, EndLine: 40, Text: "Two MiMo deployments, hardcoded.", Reason: "hit"},
		{ChunkID: 2, Repo: "rongo", Branch: "master", Path: "docs/manual-verification.md",
			StartLine: 1, EndLine: 20, Text: "Checks that need a real browser.", Reason: "hit"},
	}
}

func TestDocsOnly(t *testing.T) {
	// Given documents alone, then a mixed set, then nothing at all.
	if !DocsOnly(docSources()) {
		t.Error("two documents and no code must read as documentation-only")
	}
	mixed := append(docSources(), twoSources()[0])
	if DocsOnly(mixed) {
		t.Error("one code source is enough to make a turn not documentation-only")
	}
	// Empty is false, not true: that turn is "nothing found", which claims
	// nothing and needs no notice about its footing.
	if DocsOnly(nil) {
		t.Error("no sources at all must not read as documentation-only")
	}
}

func TestDocsOnlyNoticeFollowsTheAnswerLanguage(t *testing.T) {
	// Everything a person reads follows ask.Language, and the notice is
	// templated rather than written by a model.
	for _, lang := range []Language{LanguageEN, LanguageDE, LanguageFR, LanguageIT} {
		got := ScopeNotice(lang, Scope{DocsOnly: true})
		if got == "" {
			t.Errorf("no documentation-only notice in %s", lang)
		}
		if got != docsOnlyNotice[lang] {
			t.Errorf("notice in %s = %q, want the templated sentence", lang, got)
		}
	}
	if strings.Contains(ScopeNotice(LanguageDE, Scope{DocsOnly: true}), "ß") {
		t.Error("the German notice must use Swiss orthography")
	}
	// An unknown language falls back to English, the way ParseLanguage does.
	if got := ScopeNotice(Language("xx"), Scope{DocsOnly: true}); got != docsOnlyNotice[LanguageEN] {
		t.Errorf("fallback notice = %q", got)
	}
	// Nothing to say about the footing, nothing said.
	if got := ScopeNotice(LanguageEN, Scope{Known: []string{"rongo"}}); got != "" {
		t.Errorf("notice = %q, want none on an ordinary turn", got)
	}
}

func TestScopeNoticeSaysBothThingsWhenBothApply(t *testing.T) {
	// A question can name a repository the index lacks AND end up answered
	// from documentation. Neither sentence may swallow the other, and scope
	// comes first: which repositories were searched, then what was found.
	got := ScopeNotice(LanguageEN, Scope{Known: []string{"rongo"}, Unknown: []string{"loom"}, DocsOnly: true})
	if !strings.Contains(got, "No repository called loom") {
		t.Errorf("notice = %q, want the missing repository named", got)
	}
	if !strings.Contains(got, "documentation alone") {
		t.Errorf("notice = %q, want the documentation-only sentence", got)
	}
	if strings.Index(got, "No repository") > strings.Index(got, "documentation alone") {
		t.Errorf("notice = %q, want the scope sentence first", got)
	}
}

func TestAnswerPromptCarriesTheDocumentationOnlyRule(t *testing.T) {
	// Without it the model is handed a README and writes what it says as
	// though it had read the code. Nothing is invented — the document really
	// does say it, and may have said it for a year while the code moved.
	c, prompt, _ := streamUpstream(t, "x")
	_, err := NewAnswerer(c).Answer(context.Background(), "How are the models chosen?", AudienceBA, LanguageEN,
		docSources(), Scope{}, nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !strings.Contains(*prompt, "Every source here is a documentation file") {
		t.Errorf("the documentation-only rule is missing from the prompt:\n%s", *prompt)
	}
}

func TestAnswerPromptOmitsTheRuleWhenCodeIsPresent(t *testing.T) {
	// One code source is enough: there is something to check the documents
	// against, and the ordinary "side with the code" rule is the right one.
	c, prompt, _ := streamUpstream(t, "x")
	mixed := append(docSources(), twoSources()[0])
	_, err := NewAnswerer(c).Answer(context.Background(), "How are the models chosen?", AudienceBA, LanguageEN,
		mixed, Scope{}, nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Contains(*prompt, "Every source here is a documentation file") {
		t.Errorf("the documentation-only rule fired with code in the sources:\n%s", *prompt)
	}
}

func TestPipelineSaysWhenTheTurnStoodOnDocumentationAlone(t *testing.T) {
	// The prompt rule that the code decides where a document disagrees cannot
	// fire on a context holding no code to disagree with. The reader is the
	// only one left who can weigh it, so the turn has to say so.
	db := gatherDB(t)
	var notices []string
	c := twoStepUpstream(t, `{"intent":"how","terms":["models"],"code_terms":[],"repos":[]}`,
		"AGENTS.md states two deployments [1].")
	p := NewPipeline(c, &fakeSearch{hits: docOnlyHits()},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, _, err := p.Run(context.Background(), "How are the models chosen?", AudienceBA, LanguageEN,
		Events{OnNotice: func(text string) { notices = append(notices, text) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(notices) != 1 {
		t.Fatalf("notices = %v, want exactly one", notices)
	}
	if !strings.Contains(notices[0], "documentation alone") {
		t.Errorf("notice = %q, want the documentation-only sentence", notices[0])
	}
	// Carried out of the turn, not only sent: the record is what a resume and
	// a re-explain rebuild the same sentence from.
	if !got.Scope.DocsOnly {
		t.Error("scope.DocsOnly = false, want the flag carried out of the turn")
	}
}

func TestPipelineSaysNothingWhenTheTurnHadCode(t *testing.T) {
	// A notice on every question would be noise, and the one that matters
	// would stop being read.
	db := gatherDB(t)
	var notices []string
	hits := append(docOnlyHits(),
		retrieve.Hit{ChunkID: 3, Repo: "peeq", Path: "internal/llm/client.go", RawText: "func New() {}"})
	c := twoStepUpstream(t, `{"intent":"how","terms":["models"],"code_terms":[],"repos":[]}`, "x")
	p := NewPipeline(c, &fakeSearch{hits: hits},
		NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, _, err := p.Run(context.Background(), "How are the models chosen?", AudienceBA, LanguageEN,
		Events{OnNotice: func(text string) { notices = append(notices, text) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(notices) != 0 {
		t.Errorf("notices = %v, want none", notices)
	}
	if got.Scope.DocsOnly {
		t.Error("scope.DocsOnly = true with code in the sources")
	}
}

func TestResumedTurnSaysItStoodOnDocumentationAlone(t *testing.T) {
	// A resumed turn carries the clarification's scope, which was stored
	// before anything was gathered and can only ever say false. Computing the
	// flag in Run alone would drop the notice on exactly the turns a card sent
	// the reader into a single module.
	db := gatherDB(t)
	var notices []string
	c := twoStepUpstream(t, "{}", "AGENTS.md states two deployments [1].")
	p := NewPipeline(c, &fakeSearch{}, NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 5000}), &fakeRouter{})

	got, err := p.Resume(context.Background(), "How are the models chosen?", AudienceBA, LanguageEN,
		docOnlyHits(), Scope{}, Events{OnNotice: func(text string) { notices = append(notices, text) }})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if len(notices) != 1 || !strings.Contains(notices[0], "documentation alone") {
		t.Fatalf("notices = %v, want the documentation-only sentence", notices)
	}
	// Resume must also carry its scope out, or the record keeps the one stored
	// before the gather and a re-explain drops the notice.
	if !got.Scope.DocsOnly {
		t.Error("scope.DocsOnly = false, want the resumed turn to carry its own footing out")
	}
}
