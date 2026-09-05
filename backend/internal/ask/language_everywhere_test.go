package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// Everything a person reads follows the language they asked for: not only the
// answer, but the card that asks back, the title in the rail and the
// nothing-found text. A German question answered under English candidate
// names reads as half-translated, which is what this file pins.

func TestRouteNamesTheCandidatesInTheReadersLanguage(t *testing.T) {
	var namePrompts []string
	llmFake := testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		namePrompts = append(namePrompts, prompt)
		return `{"title":"HTTP-Schicht","summary":"Nimmt Anfragen an."}`
	})
	r := newTestRouter(t, llmFake, testDBWithDeps(t, nil))

	got, err := r.Route(context.Background(), "wie wird angemeldet?", AudienceDev, LanguageDE, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "loom", Path: "backend/internal/auth/session.go", Score: 0.49},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !got.Ask {
		t.Fatal("two unrelated candidates at the same score are a question")
	}
	if len(namePrompts) == 0 {
		t.Fatal("no naming call was made")
	}
	for _, p := range namePrompts {
		if !strings.Contains(p, "in German") {
			t.Errorf("naming prompt never names the language:\n%s", p)
		}
	}
}

func TestTitleIsWrittenInTheReadersLanguage(t *testing.T) {
	var prompt string
	c := testLLM(t, func(p string) string {
		prompt = p
		return "Anmeldung im Backend"
	})

	got := Title(context.Background(), c, "wie wird angemeldet?", LanguageDE)

	if got != "Anmeldung im Backend" {
		t.Errorf("title = %q", got)
	}
	if !strings.Contains(prompt, "in German") {
		t.Errorf("title prompt never names the language:\n%s", prompt)
	}
}

func TestNothingFoundSpeaksTheReadersLanguage(t *testing.T) {
	got := NothingFound(LanguageDE, []string{"airplay", "apple tv"})
	if !strings.Contains(got, "nichts gefunden") || !strings.Contains(got, "Gesucht nach: airplay · apple tv.") {
		t.Errorf("text = %q", got)
	}
	if !strings.Contains(NothingFound(LanguageEN, nil), "found nothing") {
		t.Errorf("text = %q", NothingFound(LanguageEN, nil))
	}
	// An unknown language falls back to English rather than to an empty string.
	if NothingFound(Language("xx"), nil) != nothingFound[LanguageEN] {
		t.Errorf("unknown language: %q", NothingFound(Language("xx"), nil))
	}
}

func TestAnswer_theLanguageIsSaidLastAsWell(t *testing.T) {
	// The sources sit between the first instruction and the answer; a model
	// that has just read two thousand tokens of English tends to answer in
	// it. The closing line is what keeps a German answer German.
	c, prompt, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(c).Answer(context.Background(), "Wie?", AudienceBA, LanguageDE, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Count(*prompt, "German") < 2 {
		t.Errorf("the language is named %d times, want it first and last:\n%s", strings.Count(*prompt, "German"), *prompt)
	}
}

// German is written the Swiss way. The static German strings already are, so a
// model that writes "größer" next to them is a visible seam. The clause rides
// on the language rather than on the step: everything a person reads carries
// it, and no other language pays for it.

func TestAnswer_germanAsksForSwissOrthography(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(c).Answer(context.Background(), "Wie?", AudienceBA, LanguageDE, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !strings.Contains(*prompt, "never the letter ß") {
		t.Errorf("the German answer prompt does not ask for Swiss orthography:\n%s", *prompt)
	}

	// An Italian answer has no ß to avoid.
	c, itPrompt, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(c).Answer(context.Background(), "Come?", AudienceBA, LanguageIT, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Contains(*itPrompt, "Swiss orthography") {
		t.Errorf("the Italian prompt carries the German orthography note:\n%s", *itPrompt)
	}
}

func TestTitleAndCandidateNamesAskForSwissOrthography(t *testing.T) {
	var titlePrompt string
	tc := testLLM(t, func(p string) string {
		titlePrompt = p
		return "Anmeldung im Backend"
	})
	Title(context.Background(), tc, "wie wird angemeldet?", LanguageDE)
	if !strings.Contains(titlePrompt, "never the letter ß") {
		t.Errorf("the German title prompt does not ask for Swiss orthography:\n%s", titlePrompt)
	}

	var namePrompts []string
	llmFake := testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		namePrompts = append(namePrompts, prompt)
		return `{"title":"HTTP-Schicht","summary":"Nimmt Anfragen an."}`
	})
	r := newTestRouter(t, llmFake, testDBWithDeps(t, nil))
	if _, err := r.Route(context.Background(), "wie wird angemeldet?", AudienceDev, LanguageDE, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "loom", Path: "backend/internal/auth/session.go", Score: 0.49},
	}, nil, false); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(namePrompts) == 0 {
		t.Fatal("no naming call was made")
	}
	for _, p := range namePrompts {
		if !strings.Contains(p, "never the letter ß") {
			t.Errorf("the German naming prompt does not ask for Swiss orthography:\n%s", p)
		}
	}
}

func TestLanguageStyle_onlyGermanCarriesOne(t *testing.T) {
	if languageStyle(LanguageDE) == "" {
		t.Error("German has no style note")
	}
	for _, l := range []Language{LanguageEN, LanguageFR, LanguageIT, Language("xx")} {
		if languageStyle(l) != "" {
			t.Errorf("%s carries a style note: %q", l, languageStyle(l))
		}
	}
}
