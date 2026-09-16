package eval

import (
	"regexp"
	"strings"
	"testing"
)

// germanDigraphs lists the words of a German answer's prose that spell an
// umlaut as ae/oe/ue. The Swiss note forbids that and the harness had no
// number for it: "die Korrektheit der Rueckgabe" passed every rubric. Fenced
// and inline code, and the citation markers, are dropped first, because code
// keeps the source's spelling by rule; what remains is running text, where
// every hit is a defect a reader sees. Words that carry the digraph
// legitimately (neue, Steuer, aktuell) are allowlisted by stem.
func germanDigraphs(answer string) []string {
	text := fencedCode.ReplaceAllString(answer, " ")
	text = inlineCode.ReplaceAllString(text, " ")
	text = citeMarker.ReplaceAllString(text, " ")
	var hits []string
	for _, w := range digraphWord.FindAllString(text, -1) {
		if legitimateDigraph(w) {
			continue
		}
		hits = append(hits, w)
	}
	return hits
}

var (
	fencedCode  = regexp.MustCompile("(?s)```.*?```")
	inlineCode  = regexp.MustCompile("`[^`\n]*`")
	citeMarker  = regexp.MustCompile(`\[\d+\]`)
	digraphWord = regexp.MustCompile(`[A-Za-zÄÖÜäöü]*(ae|oe|ue|Ae|Oe|Ue)[A-Za-zÄÖÜäöü]*`)
)

// legitimateStems are German words, and names German borrows, whose ae/oe/ue
// is not a transliterated umlaut. Compared lowercase by prefix so that the
// inflections (neue/neuen/neuer, aktuelle/aktuellen) ride along.
var legitimateStems = []string{
	"neue", "feuer", "steuer", "bauer", "dauer", "mauer", "trauer", "genau",
	"aktuell", "eventuell", "individuell", "manuell", "virtuell", "sequenziell",
	"quell", "bauen", "schauen", "israel", "michael", "poesie", "aloe", "oboe",
	"aerosol", "kanaen", "queue", "request", "true", "value", "issue",
}

func legitimateDigraph(w string) bool {
	lw := strings.ToLower(w)
	for _, s := range legitimateStems {
		if strings.HasPrefix(lw, s) {
			return true
		}
	}
	return false
}

func TestGermanDigraphs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Die Validierung laeuft in zwei Ebenen und kam zurueck [1]. `pruefeBetrag` bleibt.", []string{"laeuft", "zurueck"}},
		{"BAI sichert die Korrektheit der Rueckgabe [2].", []string{"Rueckgabe"}},
		{"Die neue Steuer ist aktuell, der Bauer schaut genauer hin.", nil},
		{"Der Wert ist true; die Queue liest den Request.", nil},
		{"Text vor dem Block.\n```java\nint fuer = 1;\n```\nDanach für.", nil},
		{"Die Prüfung läuft, die Rückgabe stimmt.", nil},
	}
	for _, c := range cases {
		got := germanDigraphs(c.in)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("germanDigraphs(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
