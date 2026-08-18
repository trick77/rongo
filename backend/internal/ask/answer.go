package ask

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/trick77/rongo/internal/llm"
)

// Audience is the altitude the answer is written at. It affects THIS step only:
// language level, depth, whether code is embedded. Everything before it —
// understanding, searching, gathering — is identical, which is what later makes
// "explain that as a dev" a second generation rather than a second search.
type Audience string

const (
	AudienceBA  Audience = "ba"
	AudienceDev Audience = "dev"
)

// answerMaxTokens is generous on purpose. This is the one call where a
// truncated reply is worse than a long one: it is what a person reads.
const answerMaxTokens = 4096

// Citation is one entry of the evidence panel. The branch travels with it
// because a forge URL without one may 404 off the default branch.
type Citation struct {
	Marker    int    `json:"marker"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Answer is one finished turn.
type Answer struct {
	Text      string
	Citations []Citation
	Usage     llm.Usage
	// Sources is what the answer was written from, so a caller can store it
	// and later re-explain the same turn for the other audience without
	// searching or gathering again.
	Sources []Source
}

// Answerer writes the answer. This is the only step that runs on Pro, and the
// only one that streams — because it is the only one a human reads.
type Answerer struct {
	llm *llm.Client
}

// NewAnswerer builds an Answerer.
func NewAnswerer(c *llm.Client) *Answerer {
	return &Answerer{llm: c}
}

const answerCommon = `Du erklaerst Code auf Deutsch in Schweizer Rechtschreibung. Verwende nie das Zeichen ß, immer ss.

Du bekommst nummerierte Quellen. Regeln, ohne Ausnahme:

- Jede Aussage stuetzt sich auf eine Quelle und traegt deren Marke, etwa [1].
- Erfinde nichts. Was in den Quellen nicht steht, existiert fuer dich nicht.
- Fuehrt der Mechanismus in Code, der nicht vorliegt, sage das: Aufruf und
  Konfiguration sind sichtbar, das Innere nicht.
- Ein Kommentar ist eine Behauptung, kein Beweis. Was der Code nicht einloest,
  gilt nicht — und wenn Kommentar und Code sich widersprechen, sage es.
- Nur Marken benutzen, die es gibt. Eine erfundene Nummer ist schlimmer als
  keine Marke.`

const answerBA = `
Zielgruppe: Business Analyst. Erklaere den Mechanismus in drei bis fuenf
Absaetzen, in der Sprache des Fachbereichs. Kein Quelltext, keine Signaturen,
keine Dateipfade im Fliesstext. Beantworte die Frage und hoere dann auf —
Randfaelle gehoeren in eine Nachfrage.`

const answerDev = `
Zielgruppe: Developer. Nenne Typen, Funktionen und Dateien beim Namen und
zitiere kurze Ausschnitte, wo sie die Erklaerung tragen. Beschreibe den
Kontrollfluss so, dass man ihm im Code folgen kann.`

// nothingFound is the answer when nothing was gathered. It is not an apology
// and not a guess: the caller adds the terms that were tried.
const nothingFound = "Dazu habe ich im indexierten Code nichts gefunden."

var markerRe = regexp.MustCompile(`\[(\d{1,3})\]`)

// Answer writes the answer for one turn, streaming it token by token.
//
// With nothing gathered it returns the "nothing found" answer WITHOUT calling
// the model. A model handed only a question and a system prompt answers it
// fluently from its own training, and that answer would be about some other
// codebase — the single most expensive failure this product can produce.
func (a *Answerer) Answer(ctx context.Context, question string, audience Audience,
	sources []Source, onToken func(string)) (Answer, error) {

	if len(sources) == 0 {
		return Answer{Text: nothingFound}, nil
	}

	system := answerCommon + answerBA
	if audience == AudienceDev {
		system = answerCommon + answerDev
	}

	var text strings.Builder
	usage, err := a.llm.Stream(ctx, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: renderSources(question, sources)},
	}, func(tok string) {
		// Normalised on the way through, so neither the reader nor the stored
		// record ever sees a ß. Instructing the model is not enough: it is
		// trained on German, not Swiss German, and it slips.
		tok = swiss(tok)
		text.WriteString(tok)
		if onToken != nil {
			onToken(tok)
		}
	}, llm.WithMaxTokens(answerMaxTokens))
	if err != nil {
		return Answer{}, fmt.Errorf("write the answer: %w", err)
	}
	return Answer{
		Text:      text.String(),
		Citations: citationsFor(text.String(), sources),
		Usage:     usage,
		Sources:   sources,
	}, nil
}

// renderSources numbers the gathered material. The number IS the citation
// marker, so the model never has to invent an identifier for a file.
func renderSources(question string, sources []Source) string {
	var b strings.Builder
	b.WriteString("Frage: ")
	b.WriteString(question)
	b.WriteString("\n\nQuellen:\n")
	for i, s := range sources {
		fmt.Fprintf(&b, "\n[%d] %s %s:%d-%d", i+1, s.Repo, s.Path, s.StartLine, s.EndLine)
		if s.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", s.Symbol)
		}
		if s.Reason != "" && s.Reason != "hit" {
			fmt.Fprintf(&b, " [hierher ueber %s]", strings.TrimPrefix(s.Reason, "reference:"))
		}
		b.WriteString("\n")
		b.WriteString(s.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// citationsFor resolves the markers the answer actually used.
//
// A marker with no source behind it is DROPPED. A model that cites [7] with
// three sources in front of it made the number up, and turning that into an
// entry in the evidence panel would put a fabricated reference under an answer
// — worse than no marker at all, because it looks checkable.
func citationsFor(text string, sources []Source) []Citation {
	used := map[int]bool{}
	// Code blocks are excluded first. The DEV prompt asks for short snippets,
	// and `args[1]` or `parts[2]` inside one is an index expression, not a
	// citation — minting an entry for it would put a reference under the answer
	// that the model never made, which is the same fabrication this function
	// exists to prevent.
	for _, m := range markerRe.FindAllStringSubmatch(withoutCode(text), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || n > len(sources) {
			continue
		}
		used[n] = true
	}
	out := make([]Citation, 0, len(used))
	for n := range used {
		s := sources[n-1]
		out = append(out, Citation{
			Marker: n, Repo: s.Repo, Branch: s.Branch, Path: s.Path,
			StartLine: s.StartLine, EndLine: s.EndLine,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Marker < out[j].Marker })
	return out
}

// withoutCode blanks fenced blocks and inline spans so a marker-shaped
// expression inside code cannot be read as a citation. Blanked rather than
// removed, because nothing else here depends on offsets and a blank keeps the
// surrounding prose intact.
func withoutCode(s string) string {
	var b strings.Builder
	rest := s
	for {
		open := strings.Index(rest, "```")
		if open < 0 {
			break
		}
		b.WriteString(rest[:open])
		rest = rest[open+3:]
		if end := strings.Index(rest, "```"); end >= 0 {
			rest = rest[end+3:]
		} else {
			rest = "" // an unclosed fence swallows the remainder, as it should
		}
	}
	b.WriteString(rest)
	return inlineCodeRe.ReplaceAllString(b.String(), " ")
}

var inlineCodeRe = regexp.MustCompile("`[^`\n]*`")

// swiss applies Swiss orthography. ß maps to ss in every position, which is the
// whole of the rule.
func swiss(s string) string { return strings.ReplaceAll(s, "ß", "ss") }
