package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// The judge decides whether the CODE is ambiguous. It has no idea who is
// going to read the card. An Analyst handed a choice between two packages
// that do the same thing for the business cannot answer it — and a question
// nobody can answer ends the turn with nothing, which is worse than an answer
// covering both. This file pins the rung that catches that case, and pins the
// Developer's path as untouched by it.

// twoUnrelated is the shape every test here routes: two candidates at almost
// the same score, in repositories with no manifest edge between them, which
// is what carries the ladder past the margin and the dependency rung.
var twoUnrelated = []retrieve.Hit{
	{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
	{Repo: "loom", Path: "backend/internal/auth/session.go", Score: 0.49},
}

func TestRouteDoesNotAskTheAnalystAQuestionOnlyCodeCanAnswer(t *testing.T) {
	// Given a judge that says ask, and a role gate that says the reader
	// cannot tell the options apart without reading code
	var prompts []string
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		prompts = append(prompts, prompt)
		switch {
		case strings.Contains(prompt, judgeMarker):
			return `{"decision":"ask"}`
		case strings.Contains(prompt, choosableMarker):
			return `{"decision":"cannot"}`
		default:
			return `{"title":"Anmeldung","summary":"Meldet den Benutzer an."}`
		}
	}), testDBWithDeps(t, nil))

	// When the reader is the Analyst
	got, err := r.Route(context.Background(), "how is authentication done?", AudienceBA, LanguageEN, twoUnrelated, nil)

	// Then the turn answers across the candidates instead of asking
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("a card the Analyst cannot decide must not be asked")
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("got %d candidates, want 2 — the answer is composed from them", len(got.Candidates))
	}
	// The gate saw the card, not the module keys: naming ran before it.
	var gate string
	for _, p := range prompts {
		if strings.Contains(p, choosableMarker) {
			gate = p
		}
	}
	if gate == "" {
		t.Fatal("no role gate call was made")
	}
	if !strings.Contains(gate, "Anmeldung") {
		t.Error("the role gate must judge the named card the reader would see, not the module keys")
	}
}

func TestRouteAsksTheAnalystWhenTheOptionsDifferInTheDomain(t *testing.T) {
	// Given a gate that says the options are tellable apart by the business
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, judgeMarker):
			return `{"decision":"ask"}`
		case strings.Contains(prompt, choosableMarker):
			return `{"decision":"choose"}`
		default:
			return `{"title":"Anmeldung","summary":"Meldet den Benutzer an."}`
		}
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "how is authentication done?", AudienceBA, LanguageEN, twoUnrelated, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Error("a choice the Analyst can make is still a question")
	}
}

func TestRouteNeverShowsTheAnalystACardCarryingAPath(t *testing.T) {
	// Given one naming call that fails: that candidate keeps its module key —
	// a directory path — as its title.
	var gateCalls int
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, judgeMarker):
			return `{"decision":"ask"}`
		case strings.Contains(prompt, choosableMarker):
			gateCalls++
			return `{"decision":"choose"}`
		case strings.Contains(prompt, "loom"):
			return `not json`
		default:
			return `{"title":"Anmeldung","summary":"Meldet den Benutzer an."}`
		}
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "how is authentication done?", AudienceBA, LanguageEN, twoUnrelated, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("a candidate that could not be named carries a path as its title; the Analyst is not shown that")
	}
	if gateCalls != 0 {
		t.Errorf("made %d gate calls, want 0 — the fallback settles it without a model", gateCalls)
	}
}

func TestRouteComposesForTheAnalystWhenTheGateCannotBeRead(t *testing.T) {
	// A reply that does not decode is the safe side, and the safe side here is
	// the judge's mirrored: composing, not asking.
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, judgeMarker):
			return `{"decision":"ask"}`
		case strings.Contains(prompt, choosableMarker):
			return `sure, they are quite different`
		default:
			return `{"title":"Anmeldung","summary":"Meldet den Benutzer an."}`
		}
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "how is authentication done?", AudienceBA, LanguageEN, twoUnrelated, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got.Ask {
		t.Error("an unreadable gate reply must not produce a card")
	}
}

// TestTheDeveloperPathIsUntouchedByTheRoleRung is the one that protects the
// measured routing numbers: the Developer picks between two packages without
// effort, so no gate runs and the naming prompt is the one that was measured.
func TestTheDeveloperPathIsUntouchedByTheRoleRung(t *testing.T) {
	var prompts []string
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		prompts = append(prompts, prompt)
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return `{"title":"Session handling","summary":"Takes requests and answers them."}`
	}), testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "how is authentication done?", AudienceDev, LanguageEN, twoUnrelated, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("two unrelated candidates at the same score are a question")
	}
	// One judgement plus one naming call per candidate, and nothing else.
	if len(prompts) != 3 {
		t.Errorf("made %d model calls, want 3 (1 judge + 2 names)", len(prompts))
	}
	for _, p := range prompts {
		if strings.Contains(p, choosableMarker) {
			t.Error("the Developer's turn must never pay for the role gate")
		}
		if strings.Contains(p, "business analyst") {
			t.Error("the Developer's naming prompt is the one that was measured; it must not carry the Analyst block")
		}
	}
}

// TestTheAnalystIsNamedInDomainWordingAndInTheReadersLanguage pins the card's
// own prompt: the Analyst block is there, and the language is named first and
// last around it, the same rule the answer follows.
func TestTheAnalystIsNamedInDomainWordingAndInTheReadersLanguage(t *testing.T) {
	var namePrompts []string
	r := newTestRouter(t, testLLM(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, judgeMarker):
			return `{"decision":"ask"}`
		case strings.Contains(prompt, choosableMarker):
			return `{"decision":"choose"}`
		default:
			namePrompts = append(namePrompts, prompt)
			return `{"title":"Anmeldung","summary":"Meldet den Benutzer an."}`
		}
	}), testDBWithDeps(t, nil))

	if _, err := r.Route(context.Background(), "wie wird angemeldet?", AudienceBA, LanguageDE, twoUnrelated, nil); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(namePrompts) == 0 {
		t.Fatal("no naming call was made")
	}
	for _, p := range namePrompts {
		if !strings.Contains(p, "business analyst") {
			t.Error("the Analyst's card is named in domain wording, not by package")
		}
		if strings.Count(p, "German") < 2 {
			t.Error("the language is named first and last, around the audience block")
		}
		if !strings.Contains(p, "always ss") {
			t.Error("a German card follows Swiss orthography, like every other German string")
		}
	}
}
