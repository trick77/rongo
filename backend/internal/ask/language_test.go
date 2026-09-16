package ask

import (
	"context"
	"strings"
	"testing"
)

func TestAnswer_theLanguageReachesThePrompt(t *testing.T) {
	// Only the answer step reads the language; a prompt that ignored it would
	// make the selector decorative. An unknown value falls back to English
	// rather than failing the turn.
	cDE, promptDE, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cDE).Answer(context.Background(), "How?", AudienceBA, LanguageDE, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	cXX, promptXX, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cXX).Answer(context.Background(), "How?", AudienceBA, Language("xx"), twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if !strings.Contains(*promptDE, "Write the answer in German.") {
		t.Errorf("the German prompt never names the language:\n%s", *promptDE)
	}
	if !strings.Contains(*promptXX, "Write the answer in English.") {
		t.Errorf("an unknown language must fall back to English:\n%s", *promptXX)
	}
}

// TestTheGermanStyleNoteKeepsTheUmlauts: the ß rule alone was over-applied. A
// model told to drop one non-ASCII letter drops the rest with it, and a card
// came back offering "Sequenzdiagramm fuer Geschaeftsprozesse" — which is
// neither Swiss nor German, and which a person reads.
func TestTheGermanStyleNoteKeepsTheUmlauts(t *testing.T) {
	note := languageStyle(LanguageDE)
	if !strings.Contains(note, "always ss") {
		t.Errorf("the German note stopped asking for ss:\n%s", note)
	}
	if !strings.Contains(note, "never ae/oe/ue") {
		t.Errorf("the German note never forbids transliterating the umlauts:\n%s", note)
	}
	for _, u := range []string{"ä", "ö", "ü"} {
		if !strings.Contains(note, u) {
			t.Errorf("the German note never shows %q, so the rule is abstract:\n%s", u, note)
		}
	}
	if languageStyle(LanguageEN) != "" {
		t.Error("only German carries an orthography note")
	}
}

func TestParseLanguage_defaultsToEnglish(t *testing.T) {
	for in, want := range map[string]Language{"": LanguageEN, "de": LanguageDE, "fr": LanguageFR, "it": LanguageIT, "klingon": LanguageEN} {
		if got := ParseLanguage(in); got != want {
			t.Errorf("ParseLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
