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

	got, err := r.Route(context.Background(), "wie wird angemeldet?", LanguageDE, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "loom", Path: "backend/internal/auth/session.go", Score: 0.49},
	}, nil)
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
	if _, err := NewAnswerer(c).Answer(context.Background(), "Wie?", AudienceBA, LanguageDE, twoSources(), Scope{}, nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Count(*prompt, "German") < 2 {
		t.Errorf("the language is named %d times, want it first and last:\n%s", strings.Count(*prompt, "German"), *prompt)
	}
}
