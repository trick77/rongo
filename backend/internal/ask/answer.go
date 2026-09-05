package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
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

// swissGerman is appended to every prompt that writes German for a reader.
// The readers are in Switzerland, where the letter does not exist, and the
// product's own German strings are already spelled this way (scopeNotice);
// a model left to itself writes "größer" right next to them. It asks for
// standard German too: naming the country alone is an invitation to dialect.
const swissGerman = `

Swiss orthography: standard written German, never the letter ß - always ss
(ausser, grösser, heisst, Strasse). Not dialect.`

// languageStyle is the orthography note for lang, empty where there is none.
func languageStyle(lang Language) string {
	if ParseLanguage(string(lang)) == LanguageDE {
		return swissGerman
	}
	return ""
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
	// Scope is what the question said about repositories, as the index
	// resolved it. Carried out of the turn so the caller can store it: the
	// sentence a reader sees is rendered from it, and a later resume or
	// re-explain rebuilds the same prompt rules from it.
	Scope Scope
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
- The same for documentation files (README, AGENTS.md, docs, markdown): they
  supply intent and context the code alone does not show, but the code decides
  what actually happens. Where a document and the code disagree, say so and
  side with the code.
- Documentation is written once and the code moves on, so a document may be
  out of date without saying so. A claim resting only on a documentation
  source names that in the sentence that makes it - what the document states,
  not what the system does - and says the code for it is not among the
  sources.
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

// answerCompare is added when the question named two or more repositories the
// index carries. It has to be explicit about covering every one of them
// because answerBA's "answer the question and then stop" reads, on its own,
// as licence to explain the first mechanism and finish — which on a question
// that named both sides is half an answer.
//
// It takes the named repositories as its one format argument, spelled the way
// the index spells them.
const answerCompare = `

The question names these repositories: %s. Each has its own implementation and
all of them are in the sources. Cover every one of them, say plainly where they
differ and where they agree, and attribute every claim to the repository it
came from. Do not answer for one and leave the others out; do not merge them
into a single mechanism they do not share. Repository names stay as they are.`

// answerMissingRepo is added when the question named a repository the index
// does not carry. Without it the model is handed "how do loom and rongo
// differ" plus rongo-only sources, and writes loom's side from its own
// training — the invention the whole prompt is built to prevent.
const answerMissingRepo = `

The question names repositories that are NOT indexed: %s. There are no sources
for them and you know nothing about them. Say in one sentence that they are not
in the index, answer for the rest, and make no claim of any kind about their
code - not a guess, not a comparison, not "presumably".`

// answerDocsOnly is added when every source is a documentation file. Without
// it the model is handed a README and writes what the README says as though it
// had read the code — the failure this whole prompt exists to prevent, in the
// one shape the "invent nothing" rule does not catch, because nothing is
// invented: the document really does say it, and may have said it for a year
// while the code moved.
const answerDocsOnly = `

Every source here is a documentation file. There is no code in front of you for
this question. Report what the documents state, attributed to them, and say in
one sentence that the code was not among the sources - so what they describe is
the intent on record, not verified behaviour. Make no claim about how the code
actually works, and do not present a document's description as the mechanism.`

const answerDev = `
Audience: developer. Name types, functions and files, and quote short excerpts
where they carry the explanation. A fenced code block carries its language tag
(` + "```go" + `, ` + "```typescript" + `), never a bare ` + "```" + `. Describe
the control flow so that it can be followed in the code.`

// answerDiagram follows the audience block, so "the audience rules above"
// are the ones the model just read: a Developer diagram names functions and
// files, an Analyst diagram speaks the domain. The fence is named literally,
// as the DEV block names ` + "```go" + `: the model needs the syntax, not a
// description of it. The src array cites the same sources the prose does,
// but it is JSON: answerCommon's "one marker per bracket" reads as [6][25]
// applied to an array, which is not JSON at all, so the fence says outright
// that the rule stops here.
//
// The counts below are what reads well, and the renderer no longer enforces
// them: a spec one actor too wide used to be dropped and shown as its JSON,
// which is worse than a wide picture in a box that scrolls. They stay strict
// here because that is what keeps a diagram compact in the first place.
const answerDiagram = `

At most one diagram, and only where control flow or a call sequence carries
the explanation: a fenced block tagged ` + "```diagram" + ` holding JSON, either
{"type":"flow","nodes":[{"id","label","kind":"start|end|step|decision","src":[1]}],
 "edges":[{"from","to","label"}]} or
{"type":"sequence","actors":[{"id","label"}],
 "steps":[{"from","to","label","kind":"call|return|async","src":[1]}]}.
src holds the markers the node rests on. It is a JSON array, not prose: two
sources read "src":[6,25], never "src":[6][25] - the one-marker-per-bracket
rule is about running text and does not reach inside the fence. At most
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

// Scope is what the question said about repositories, after the index has
// been asked which of those names it carries. It travels from the pipeline to
// the answer prompt and, for Unknown, to the reader.
type Scope struct {
	// Known are the named repositories the index carries, in the order the
	// question named them. Two or more means the turn is a comparison.
	Known []string
	// Unknown are the named repositories the index does not carry. The search
	// silently ignores them — it has to, or a mishearing would wipe the whole
	// result — so this is the only thing that keeps a turn from answering
	// about code the reader never asked about.
	Unknown []string
	// All is the reader asking for the whole corpus on purpose — either the
	// question said so, or they picked "all repositories" off a repository
	// card. It is what tells the repository rung in Decide that a turn
	// spanning several repositories is wanted rather than ambiguous, and it is
	// part of the record: a resumed or re-explained turn must answer under the
	// same permission the first one had.
	All bool `json:"All,omitempty"`
	// DocsOnly is true when every source the answer was written from is
	// documentation. Not something the question said, but it belongs here for
	// the same reason Unknown does: it is what the turn has to tell the reader
	// about its own footing, and it has to survive into the record so a resume
	// and a re-explain rebuild the same notice and the same prompt rule.
	//
	// The column is a JSON blob, so an older row simply decodes to false and
	// no migration is needed.
	DocsOnly bool `json:"docs_only,omitempty"`
}

// DocsOnly reports whether every source is documentation — prose about the
// mechanism, with none of the mechanism itself in front of the model.
//
// Empty is FALSE, not true: a turn with no sources at all is the "nothing
// found" answer, which says what it tried and claims nothing.
func DocsOnly(sources []Source) bool {
	if len(sources) == 0 {
		return false
	}
	for _, s := range sources {
		if !retrieve.IsDocPath(s.Path) {
			return false
		}
	}
	return true
}

// scopeNotice is the "one of the repositories you named is not indexed"
// sentence, in the language the reader asked for. Templated rather than
// written by a model, exactly like nothingFound: a person reads it, so the
// language invariant applies, and no model call is worth spending on a
// sentence whose content is already known.
//
// Two format arguments: the missing names, then the ones actually searched.
var scopeNotice = map[Language]string{
	LanguageEN: "No repository called %s in the index. Answered for %s alone.",
	LanguageDE: "Kein Repository namens %s im Index. Nur %s beantwortet.",
	LanguageFR: "Aucun dépôt nommé %s dans l'index. Réponse portant sur %s uniquement.",
	LanguageIT: "Nessun repository di nome %s nell'indice. Risposta solo su %s.",
}

// scopeNoticeWhole is the same sentence when the question named nothing the
// index carries: there is no narrowed scope to name, so the turn searched
// everything.
var scopeNoticeWhole = map[Language]string{
	LanguageEN: "No repository called %s in the index. Searched all indexed repositories.",
	LanguageDE: "Kein Repository namens %s im Index. Alle indexierten Repositories durchsucht.",
	LanguageFR: "Aucun dépôt nommé %s dans l'index. Recherche sur tous les dépôts indexés.",
	LanguageIT: "Nessun repository di nome %s nell'indice. Cercato in tutti i repository indicizzati.",
}

// docsOnlyNotice is the "this answer stood on documentation alone" sentence.
// Templated for the same reason scopeNotice is: a person reads it, so the
// language invariant applies, and its content is already known.
//
// It says the second half — that documentation can lag — because the first
// half alone reads as a footnote about provenance. What the reader has to take
// away is that nothing verified the document, which is the difference between
// a citation and a guarantee.
var docsOnlyNotice = map[Language]string{
	LanguageEN: "Answered from documentation alone: no code for this was among the sources. Documentation can lag the code it describes.",
	LanguageDE: "Nur aus der Dokumentation beantwortet: Zu dieser Frage lag kein Code in den Quellen. Dokumentation kann dem Code hinterherhinken.",
	LanguageFR: "Réponse fondée sur la seule documentation : aucun code correspondant ne figurait dans les sources. La documentation peut être en retard sur le code.",
	LanguageIT: "Risposta basata solo sulla documentazione: nessun codice corrispondente era tra le fonti. La documentazione può essere più vecchia del codice.",
}

// ScopeNotice is what the reader is told above the answer about the turn's own
// footing: a repository the index does not carry, an answer that stood on
// documentation alone, or "" when neither applies — which is the ordinary
// case, and says nothing.
//
// It takes the whole Scope rather than its parts so that every caller —
// the pipeline, a resume and a re-explain — renders from one value and none of
// them has to be found again when the Scope grows a field.
//
// Both sentences applying is not a contradiction and both are said, scope
// first: which repositories were searched comes before what was found in them.
func ScopeNotice(lang Language, sc Scope) string {
	l := ParseLanguage(string(lang))
	var parts []string
	if len(sc.Unknown) > 0 {
		missing := strings.Join(sc.Unknown, ", ")
		if len(sc.Known) == 0 {
			parts = append(parts, fmt.Sprintf(scopeNoticeWhole[l], missing))
		} else {
			parts = append(parts, fmt.Sprintf(scopeNotice[l], missing, strings.Join(sc.Known, ", ")))
		}
	}
	if sc.DocsOnly {
		parts = append(parts, docsOnlyNotice[l])
	}
	return strings.Join(parts, " ")
}

// allReposTitle and allReposSummary are the last entry on a repository card:
// the reader saying "I meant all of them". Templated rather than written by a
// model, exactly like scopeNotice and nothingFound — the text is already
// known, and a person reads it, so the answer language applies.
var allReposTitle = map[Language]string{
	LanguageEN: "All repositories",
	LanguageDE: "Alle Repositories",
	LanguageFR: "Tous les dépôts",
	LanguageIT: "Tutti i repository",
}

var allReposSummary = map[Language]string{
	LanguageEN: "Answer across every indexed repository.",
	LanguageDE: "Über alle indexierten Repositories hinweg antworten.",
	LanguageFR: "Répondre sur l'ensemble des dépôts indexés.",
	LanguageIT: "Rispondere su tutti i repository indicizzati.",
}

// AllReposChoice is the title and summary of a repository card's last entry,
// in the language the reader asked for.
func AllReposChoice(lang Language) (title, summary string) {
	l := ParseLanguage(string(lang))
	return allReposTitle[l], allReposSummary[l]
}

// coveredRepos is the named repositories that actually have a source in front
// of the model, in the order the question named them.
func coveredRepos(known []string, sources []Source) []string {
	if len(known) == 0 {
		return nil
	}
	has := make(map[string]bool, len(sources))
	for _, s := range sources {
		has[s.Repo] = true
	}
	var out []string
	for _, n := range known {
		if has[n] {
			out = append(out, n)
		}
	}
	return out
}

// Answer writes the answer for one turn, streaming it token by token.
//
// With nothing gathered it returns the "nothing found" answer WITHOUT calling
// the model. A model handed only a question and a system prompt answers it
// fluently from its own training, and that answer would be about some other
// codebase — the single most expensive failure this product can produce.
func (a *Answerer) Answer(ctx context.Context, question string, audience Audience, lang Language,
	sources []Source, scope Scope, onToken func(string)) (Answer, error) {

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
	// After the audience block, so "cover every one of them" is read against
	// the shape the audience block just set rather than before it.
	//
	// Against the repositories the SOURCES actually cover, never against the
	// names alone: a named repository can be indexed, be searched on its own,
	// and still return nothing for this question. Telling the model to cover
	// it anyway is an instruction to invent, which is the one thing the rest
	// of this prompt exists to prevent.
	if covered := coveredRepos(scope.Known, sources); len(covered) >= 2 {
		system += fmt.Sprintf(answerCompare, strings.Join(covered, ", "))
	}
	if len(scope.Unknown) > 0 {
		system += fmt.Sprintf(answerMissingRepo, strings.Join(scope.Unknown, ", "))
	}
	// Computed from the sources rather than read off the scope, so this block
	// is right even for a caller that has not filled the field in.
	if DocsOnly(sources) {
		system += answerDocsOnly
	}
	system += answerDiagram
	// Said twice, first and at the end: the sources in between are code and
	// comments in whatever language the repository uses, and a model that has
	// just read two thousand tokens of English tends to answer in it. How that
	// language is spelled follows it, closing the prompt.
	system += fmt.Sprintf(answerLanguage, name)
	system += languageStyle(lang)

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
