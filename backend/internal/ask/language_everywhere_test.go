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

// German is written the Swiss way by rewriting what the model wrote, not by
// asking it (swiss.go): the static German strings are already spelled so, and
// a model that writes "größer" next to them is a visible seam. The record is
// the corrected text, and no other language is touched.

func TestAnswer_germanIsSpelledTheSwissWay(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "Der Gesch", "aeftsprozess ist gr", "ößer [1].")
	var seen []string
	a, err := NewAnswerer(c).Answer(context.Background(), "Wie?", AudienceBA, LanguageDE, twoSources(), Scope{}, "", collect(&seen))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if a.Text != "Der Geschäftsprozess ist grösser [1]." {
		t.Errorf("Text = %q", a.Text)
	}
	if got := strings.Join(seen, ""); got != a.Text {
		t.Errorf("the reader saw %q, the record is %q", got, a.Text)
	}
	if got := strings.Join(a.Respelled, ","); got != "Geschaeftsprozess,größer" {
		t.Errorf("Respelled = %q", got)
	}
	if strings.Contains(*prompt, "Swiss") || strings.Contains(*prompt, "ß") {
		t.Errorf("the spelling is a string function, not a prompt note:\n%s", *prompt)
	}

	// An Italian answer is what the model wrote.
	c, _, _ = streamUpstream(t, "Il Gesch", "aeftsprozess è größer.")
	a, err = NewAnswerer(c).Answer(context.Background(), "Come?", AudienceBA, LanguageIT, twoSources(), Scope{}, "", nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if a.Text != "Il Geschaeftsprozess è größer." || a.Respelled != nil {
		t.Errorf("Italian was respelled: %q %v", a.Text, a.Respelled)
	}
}

func TestTitleAndCandidateNamesAreSpelledTheSwissWay(t *testing.T) {
	tc := testLLM(t, func(_ string) string { return "Anmeldung fuer die Straße" })
	if got := Title(context.Background(), tc, "wie wird angemeldet?", LanguageDE); got != "Anmeldung für die Strasse" {
		t.Errorf("Title() = %q", got)
	}
	if got := Title(context.Background(), tc, "how?", LanguageEN); got != "Anmeldung fuer die Straße" {
		t.Errorf("an English title was respelled: %q", got)
	}

	llmFake := testLLM(t, func(prompt string) string {
		if strings.Contains(prompt, judgeMarker) {
			return `{"decision":"ask"}`
		}
		return `{"title":"HTTP-Schicht fuer Anfragen","summary":"Nimmt größere Anfragen an."}`
	})
	r := newTestRouter(t, llmFake, testDBWithDeps(t, nil))
	res, err := r.Route(context.Background(), "wie wird angemeldet?", AudienceDev, LanguageDE, []retrieve.Hit{
		{Repo: "peeq", Path: "backend/internal/auth/session.go", Score: 0.50},
		{Repo: "loom", Path: "backend/internal/auth/session.go", Score: 0.49},
	}, nil, false)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !res.Ask || len(res.Candidates) == 0 {
		t.Fatal("no card was asked")
	}
	named := 0
	for _, c := range res.Candidates {
		if c.ModuleKey == "" && c.Repo == "" {
			continue // the all-projects entry is a static string, not a naming call
		}
		named++
		if c.Title != "HTTP-Schicht für Anfragen" || c.Summary != "Nimmt grössere Anfragen an." {
			t.Errorf("card = %q / %q", c.Title, c.Summary)
		}
	}
	if named == 0 {
		t.Error("no named candidate on the card")
	}
}
