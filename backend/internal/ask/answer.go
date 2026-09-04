package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// Language is the language everything a person reads is written in: the
// answer, the clarification card's titles and summaries, the thread title and
// the nothing-found text. Model-internal steps (understanding, search terms,
// the judge) stay English. An unknown value is never an error — ParseLanguage
// falls back to English, the same way an unknown audience falls back to BA.
type Language string

const (
	LanguageEN Language = "en"
	LanguageDE Language = "de"
	LanguageFR Language = "fr"
	LanguageIT Language = "it"
)

// languageNames is the allowlist, and the word the prompt uses for each entry.
var languageNames = map[Language]string{
	LanguageEN: "English",
	LanguageDE: "German",
	LanguageFR: "French",
	LanguageIT: "Italian",
}

// ParseLanguage maps a wire value onto the allowlist, defaulting to English.
func ParseLanguage(s string) Language {
	if _, ok := languageNames[Language(s)]; ok {
		return Language(s)
	}
	return LanguageEN
}

// languageName is the word a prompt uses for lang, after the same fallback
// ParseLanguage applies.
func languageName(lang Language) string {
	return languageNames[ParseLanguage(string(lang))]
}

// answerMaxTokens is generous on purpose. This is the one call where a
// truncated reply is worse than a long one: it is what a person reads.
//
// The budget is shared with the model's reasoning: max_completion_tokens
// counts the hidden thinking as well as the visible answer. At 4096 a DEV
// re-explain over 261 sources spent the whole budget thinking and wrote
// nothing (thread 2, message 5, 2026-09-03). The value here is a judgment,
// not a measurement; a length failure now logs its completion count, which
// is the number to calibrate against.
const answerMaxTokens = 16384

// Citation is one entry of the evidence panel. The branch travels with it
// because a forge URL without one may 404 off the default branch. The SHA is
// the commit the cited file was indexed at: the source viewer reads the file
// at that commit, so the cited lines are the lines the answer was written
// from even after the branch has moved on.
type Citation struct {
	Marker    int    `json:"marker"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	SHA       string `json:"sha"`
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

// answerCommon takes the language name as its one format argument. Only the
// answer is written in that language: source paths, symbols and the markers
// are quoted as they are.
const answerCommon = `You explain code. Write the answer in %s.

You are given numbered sources. The rules, without exception:

- Every statement rests on a source and carries its marker, such as [1].
- Invent nothing. What is not in the sources does not exist for you.
- If the mechanism leads into code that is not present, say so: the call and
  the configuration are visible, the inside is not.
- A comment is a claim, not proof. What the code does not deliver does not
  hold - and where comment and code contradict each other, say it.
- The same for documentation files (README, docs, markdown): they supply
  intent and context the code alone does not show, but the code decides what
  actually happens. Where a document and the code disagree, say so and side
  with the code.
- Only use markers that exist. An invented number is worse than no marker.
- One marker per bracket: a claim resting on two sources reads [1][2], never
  [1, 2].`

// answerLanguage closes the system prompt. Identifiers stay as they are: a
// translated function name is a name that does not exist.
const answerLanguage = `

Language: every sentence of the answer, headings and list items included, is
written in %s, regardless of the language of the sources or of these
instructions. Identifiers, file names, quoted code and the markers stay
exactly as they are.`

const answerBA = `
Audience: business analyst. Explain the mechanism in three to five paragraphs,
in the language of the business domain. No source code, no signatures, no file
paths in running text. Answer the question and then stop - edge cases belong in
a follow-up.`

const answerDev = `
Audience: developer. Name types, functions and files, and quote short excerpts
where they carry the explanation. A fenced code block carries its language tag
(` + "```go" + `, ` + "```typescript" + `), never a bare ` + "```" + `. Describe
the control flow so that it can be followed in the code.`

// answerDiagram follows the audience block, so "the audience rules above"
// are the ones the model just read: a Developer diagram names functions and
// files, an Analyst diagram speaks the domain. The fence is named literally,
// as the DEV block names ` + "```go" + `: the model needs the syntax, not a
// description of it. The src array is the marker syntax the prose uses.
const answerDiagram = `

At most one diagram, and only where control flow or a call sequence carries
the explanation: a fenced block tagged ` + "```diagram" + ` holding JSON, either
{"type":"flow","nodes":[{"id","label","kind":"start|end|step|decision","src":[1]}],
 "edges":[{"from","to","label"}]} or
{"type":"sequence","actors":[{"id","label"}],
 "steps":[{"from","to","label","kind":"call|return|async","src":[1]}]}.
src lists the markers the node rests on, as a claim in prose would. At most
12 nodes, 5 actors, 12 steps. Labels follow the audience rules above and are
written in the answer language; ids stay short ASCII. The prose still
explains; the diagram is not a substitute.`

// nothingFound is the answer when nothing was gathered, in the language the
// reader asked for. It is not an apology and not a guess: the caller adds the
// terms that were tried. Fixed text rather than a model call: an answer with
// no sources must never come from a model.
var nothingFound = map[Language]string{
	LanguageEN: "I found nothing about this in the indexed code.",
	LanguageDE: "Dazu habe ich im indexierten Code nichts gefunden.",
	LanguageFR: "Je n'ai rien trouvé à ce sujet dans le code indexé.",
	LanguageIT: "Non ho trovato nulla al riguardo nel codice indicizzato.",
}

// searchedFor introduces the terms that were tried, in the same language.
var searchedFor = map[Language]string{
	LanguageEN: "Searched for",
	LanguageDE: "Gesucht nach",
	LanguageFR: "Recherché ",
	LanguageIT: "Cercato",
}

// NothingFound is the "nothing found" answer for lang, with the terms that
// were tried appended when there are any.
func NothingFound(lang Language, terms []string) string {
	l := ParseLanguage(string(lang))
	if len(terms) == 0 {
		return nothingFound[l]
	}
	return nothingFound[l] + " " + searchedFor[l] + ": " + strings.Join(terms, " · ") + "."
}

// Answer writes the answer for one turn, streaming it token by token.
//
// With nothing gathered it returns the "nothing found" answer WITHOUT calling
// the model. A model handed only a question and a system prompt answers it
// fluently from its own training, and that answer would be about some other
// codebase — the single most expensive failure this product can produce.
func (a *Answerer) Answer(ctx context.Context, question string, audience Audience, lang Language,
	sources []Source, onToken func(string)) (Answer, error) {

	if len(sources) == 0 {
		return Answer{Text: NothingFound(lang, nil)}, nil
	}

	name := languageName(lang)
	system := fmt.Sprintf(answerCommon, name)
	if audience == AudienceDev {
		system += answerDev
	} else {
		system += answerBA
	}
	system += answerDiagram
	// Said twice, first and last: the sources in between are code and comments
	// in whatever language the repository uses, and a model that has just read
	// two thousand tokens of English tends to answer in it.
	system += fmt.Sprintf(answerLanguage, name)

	// Every token passes the renumberer before it reaches the reader or the
	// record, so the two are the same text; what it holds back is flushed
	// once the stream ends, cut short or not.
	var text strings.Builder
	rn := newRenumberer(len(sources))
	emit := func(s string) {
		if s == "" {
			return
		}
		text.WriteString(s)
		if onToken != nil {
			onToken(s)
		}
	}
	usage, err := a.llm.Stream(ctx, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: renderSources(question, sources)},
	}, func(tok string) { emit(rn.feed(tok)) }, llm.WithMaxTokens(answerMaxTokens), llm.WithStep("answer"))
	emit(rn.flush())
	var cut *llm.FinishError
	if errors.As(err, &cut) && strings.TrimSpace(text.String()) != "" {
		// The upstream cut the answer short, but what arrived is what the
		// reader watched being written. It is kept, as it was before the finish
		// reason was read at all; failing the turn here would drop the text
		// from the record while the browser still shows it. The cut is logged
		// with the number that says whether the budget was the cause.
		slog.Warn("answer cut short", "finish_reason", cut.Reason, "completion_tokens", cut.Completion)
		err = nil
	}
	if err != nil {
		return Answer{}, fmt.Errorf("write the answer: %w", err)
	}
	if strings.TrimSpace(text.String()) == "" {
		// A clean stream with nothing in it is not an answer. Stored as one, it
		// was a finished turn with an empty body and no log line: the reader
		// saw a Done mark over nothing. The usage goes into the error because
		// the completion count is what says whether the budget was the cause.
		return Answer{}, fmt.Errorf("write the answer: the model returned no answer text (%d completion tokens)", usage.Completion)
	}
	return Answer{
		Text:      text.String(),
		Citations: rn.citations(sources),
		Usage:     usage,
		Sources:   sources,
	}, nil
}

// renderSources numbers the gathered material. The number IS the citation
// marker the model writes, so it never has to invent an identifier for a
// file; the renumberer turns it into the reader's number on the way out.
func renderSources(question string, sources []Source) string {
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(question)
	b.WriteString("\n\nSources:\n")
	for i, s := range sources {
		fmt.Fprintf(&b, "\n[%d] %s %s:%d-%d", i+1, s.Repo, s.Path, s.StartLine, s.EndLine)
		if s.Symbol != "" {
			fmt.Fprintf(&b, " (%s)", s.Symbol)
		}
		if s.Reason != "" && s.Reason != "hit" {
			fmt.Fprintf(&b, " [reached via %s]", strings.TrimPrefix(s.Reason, "reference:"))
		}
		b.WriteString("\n")
		b.WriteString(s.Text)
		b.WriteString("\n")
	}
	return b.String()
}
