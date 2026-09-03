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
	if _, err := NewAnswerer(cDE).Answer(context.Background(), "How?", AudienceBA, LanguageDE, twoSources(), nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	cXX, promptXX, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(cXX).Answer(context.Background(), "How?", AudienceBA, Language("xx"), twoSources(), nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if !strings.Contains(*promptDE, "Write the answer in German.") {
		t.Errorf("the German prompt never names the language:\n%s", *promptDE)
	}
	if !strings.Contains(*promptXX, "Write the answer in English.") {
		t.Errorf("an unknown language must fall back to English:\n%s", *promptXX)
	}
}

func TestParseLanguage_defaultsToEnglish(t *testing.T) {
	for in, want := range map[string]Language{"": LanguageEN, "de": LanguageDE, "fr": LanguageFR, "it": LanguageIT, "klingon": LanguageEN} {
		if got := ParseLanguage(in); got != want {
			t.Errorf("ParseLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
