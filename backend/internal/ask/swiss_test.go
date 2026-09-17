package ask

import (
	"strings"
	"testing"
)

func TestSwissWord(t *testing.T) {
	cases := map[string]string{
		// ß is always ss.
		"größer": "grösser", "Straße": "Strasse", "STRAẞE": "STRASSE", "heißt": "heisst",
		// A digraph is the umlaut it stands for.
		"fuer": "für", "ueber": "über", "Geschaeftsprozess": "Geschäftsprozess",
		"Ueberblick": "Überblick", "Aenderung": "Änderung", "Oel": "Öl",
		"Kuenstler": "Künstler", "ueberprueft": "überprüft", "Qualitaet": "Qualität",
		"Rueckgabe": "Rückgabe", "laeuft": "läuft", "enthaelt": "enthält",
		// A ue after a, e or q is two vowels.
		"neue": "neue", "Steuer": "Steuer", "Bauer": "Bauer", "Quelle": "Quelle",
		"Vertrauen": "Vertrauen", "Frequenz": "Frequenz", "bequem": "bequem", "Feuer": "Feuer",
		"Bäuerin": "Bäuerin", "säuern": "säuern",
		// A ue closing the word is English; inside a German word it is not.
		"Continue": "Continue", "Revenue": "Revenue", "Values": "Values", "Issues": "Issues",
		"duenn": "dünn", "trueb": "trüb", "Zoelle": "Zölle", "Fuesse": "Füsse",
		// The Latin suffix, the borrowings, the names.
		"aktuell": "aktuell", "manuell": "manuell", "individuelle": "individuelle", "aktuellsten": "aktuellsten",
		"Duell": "Duell", "Mueller": "Müller", "befuellen": "befüllen", "erfuellen": "erfüllen", "Huelle": "Hülle", "Fuellstand": "Füllstand", "Muell": "Müll", "Guelle": "Gülle", "visuelle": "visuelle", "graduell": "graduell",
		"Individuen": "Individuen", "Residuen": "Residuen", "Kongruenz": "Kongruenz", "Statuen": "Statuen", "Menuett": "Menuett",
		"Guest": "Guest", "Fluent": "Fluent", "does": "does", "Influencer": "Influencer",
		"zuerst": "zuerst", "Israel": "Israel", "Goethe": "Goethe", "Bluetooth": "Bluetooth",
		"Queue": "Queue", "true": "true", "Blueprint": "Blueprint", "Koeffizient": "Koeffizient", "Michael": "Michael",
		// An acronym keeps its letters.
		"VALUE": "VALUE", "AE": "AE",
		// Already right.
		"Prüfung": "Prüfung", "Zitat": "Zitat",
	}
	for in, want := range cases {
		if got := swissWord(in); got != want {
			t.Errorf("swissWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSpeller_leavesIdentifiersAlone(t *testing.T) {
	// Every one of these is a name from code, backticks or not, and renaming
	// it points at a symbol that does not exist.
	for _, u := range []string{
		"pruefeBetrag", "strasse_id", "src/ueber/x.go", "obj.groesse", "groesse.go",
		"MAX_GROESSE", "v2ueber", "user@strasse.ch", "https://ueber.example/strasse",
		"pkg.Strasse", "Strasse.Groesse",
	} {
		sp := &speller{}
		if got := sp.prose(u); got != u {
			t.Errorf("prose(%q) = %q, want it untouched", u, got)
		}
	}
}

func TestSpeller_respellsProseAndRemembers(t *testing.T) {
	sp := &speller{}
	in := "Die Validierung laeuft in zwei Ebenen (größer als zuvor) und kam zurueck.\nDie neue Steuer ist aktuell."
	want := "Die Validierung läuft in zwei Ebenen (grösser als zuvor) und kam zurück.\nDie neue Steuer ist aktuell."
	if got := sp.prose(in); got != want {
		t.Errorf("prose() =\n%s\nwant\n%s", got, want)
	}
	if got := strings.Join(sp.rewrote, ","); got != "laeuft,größer,zurueck" {
		t.Errorf("rewrote = %q", got)
	}
}

func TestSpeller_hyphenatedWordsAreTwoWords(t *testing.T) {
	sp := &speller{}
	if got := sp.prose("Rueckgabe-Wert, Ein-/Ausgabe"); got != "Rückgabe-Wert, Ein-/Ausgabe" {
		t.Errorf("prose() = %q", got)
	}
}

func TestSwissMarkdown_stepsOverCode(t *testing.T) {
	in := "Groesse in `pruefeGroesse` und\n```go\nvar groesse = strasse\n```\nist größer."
	want := "Grösse in `pruefeGroesse` und\n```go\nvar groesse = strasse\n```\nist grösser."
	if got := swissMarkdown(in); got != want {
		t.Errorf("swissMarkdown() =\n%s\nwant\n%s", got, want)
	}
}

func TestSpellFor_onlyGermanIsRespelled(t *testing.T) {
	if got := spellFor(LanguageDE)("größer"); got != "grösser" {
		t.Errorf("DE: %q", got)
	}
	for _, l := range []Language{LanguageEN, LanguageFR, LanguageIT, Language("xx")} {
		if got := spellFor(l)("größer fuer"); got != "größer fuer" {
			t.Errorf("%s: %q, want it untouched", l, got)
		}
	}
}
