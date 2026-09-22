package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/memory"
	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/stages"
)

// Audience is the altitude the answer is written at. It affects THIS step only:
// language level, depth, whether code is embedded. Everything before it —
// understanding, searching, gathering — is identical, which is what later makes
// "explain that as a dev" a second generation rather than a second search.
type Audience string

// The audiences an answer can be written for.
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

// The languages an answer can be written in.
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
//
// A commit citation (Kind = SourceCommit) cites a change rather than lines:
// SHA is the commit, Path and the lines are empty, and Subject and
// CommittedAt are what the chip shows. It opens in the commit view, never in
// the file viewer.
type Citation struct {
	Marker    int    `json:"marker"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	SHA       string `json:"sha"`
	Kind      string `json:"kind,omitempty"`
	Subject   string `json:"subject,omitempty"`
	// CommittedAt is RFC 3339 in UTC; empty for a file citation.
	CommittedAt string `json:"committed_at,omitempty"`
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
	// Prompt is what the call was sent, measured HERE rather than billed by
	// the endpoint: the upstream reports one prompt figure and never says
	// which part of it was the rules, which the code and which the question.
	// The parts are the same chars-per-four estimate the gatherer budgets
	// with, so they will not add up to Usage.Prompt exactly — the total is
	// billed, the split is measured, and the reader is told which is which.
	Prompt PromptParts
	// Respelled is every word of a German answer the model wrote with ß or
	// a transliterated umlaut, as the model wrote it; the reader saw the
	// Swiss spelling. The eval counts them, because the number says how the
	// deployment writes German, and nothing else does now that the text is
	// corrected on the way out.
	Respelled []string
	// Memories is how many of the reader's standing instructions the prompt
	// carried, for the trace. Zero for a reader with none.
	Memories int
}

// PromptParts is the answer prompt by section, in estimated tokens. System is
// every rule the audience, language and scope assembled — and the thread's
// previous question with it, because a follow-up is written into the rules
// (answerFollowUp) rather than into the message the reader typed. Sources is
// the code in front of the model, headers and separators included. Question
// is what was asked, and only that.
type PromptParts struct {
	System   int `json:"system"`
	Sources  int `json:"sources"`
	Question int `json:"question"`
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
//
// The rule about a statement made about the sources as a whole is there
// because "every statement carries its marker" has a reading that turns on
// itself: an off-corpus question is refused, the refusal says the sources are
// about something else, and that claim rests on all of them. One turn came
// back with every one of its 58 gathered sources in a single bracket run.
//
// Absence takes no marker at all, and that half is the strict one: a refusal
// citing three sources as examples hands the reader three chips that open
// files saying nothing about what was asked, which is the click the citation
// rules exist to prevent. Examples are for a positive characterisation.
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
- A source marked "(test)" is a test: it shows what the code is expected to
  do, not the code that does it. A claim about the mechanism cites the source
  holding the mechanism, and a test beside it at most as a second marker. A
  claim resting only on a test says so in the sentence that makes it - what
  the test expects, not what the system does. Where the question asks how
  something is tested, tests are the answer and are cited as such.
- <redacted> in a source marks a credential removed before indexing. Say that
  the key exists and where it is set; never guess, reconstruct or describe
  the value.
- Only use markers that exist. An invented number is worse than no marker.
- One marker per bracket: a claim resting on two sources reads [1][2], never
  [1, 2].
- A statement about the sources as a whole is not a claim any one passage
  makes. That they are silent on a topic, or that it is absent from them,
  carries no marker at all: never enumerate the sources to prove they are
  unrelated, and never cite a few of them as examples of the silence either.
  A statement about what they do cover may name at most three as examples.`

// answerLanguage closes the system prompt. Identifiers stay as they are: a
// translated function name is a name that does not exist.
const answerLanguage = `

Language: every sentence of the answer, headings and list items included, is
written in %s, regardless of the language of the sources or of these
instructions. Identifiers, file names, quoted code and the markers stay
exactly as they are.`

// answerBA opens with who the reader is and what a good answer gives them.
// Before it did, the block was bans only - no code, no signatures, no paths -
// and a model told what to avoid writes developer prose with the banned words
// removed: a delay question for PROD came back with the raw cron in the
// opening sentence and no rule the reader could act on.
//
// The opening describes the reader by what they decide and what they know,
// because the job title alone left what a business analyst is to the model's
// training: "does not read code" was read narrowly, and deployments and data
// models went on being explained as common knowledge. Every clause names a
// move - write in that vocabulary, say what each system does for the
// business, state the effect the business sees - since a sentence that only
// describes the reader leaves the behaviour to be inferred, and a ban gets
// prose with the banned words removed (2026-09-17-ba-diagrams.md measured the
// negative phrasing of the diagram trigger at 1 picture in 19 answers against
// 13 for the positive rewrite). What lies past the reader is the HOW, never
// the WHAT EXISTS: the nouns - configuration, services, queues - are load
// bearing in the sentences below and in the diagram trigger, and a corpus
// question names a queue outright. No clause asks for a system to be renamed
// into business words: the sources carry the code's names, and inventing one
// would decouple a claim from its citation. "The person who will read this
// answer" binds the pronoun for the blocks appended later, which say "the
// reader" with no antecedent of their own. Unmeasured: the arm was not run
// for this change. The clause to watch if it ever is, is "how the systems
// reach each other -> state the effect the business sees", which sits close
// to the crossing the corpus is built on.
//
// The first paragraph has to live with the blocks appended after it. The
// things a reader wants are listed, so the lead rule from answerShape is
// restated here - one of them opens, the rest follow - because without it the
// list lands in the opening sentence whole. "Fixed in code, configuration or manual" is
// conditioned on the sources because answerCommon says invent nothing and an
// unconditioned rule classifies without evidence. "Condition" and "edge case"
// are told apart in the text because both words stay in the block. No domain
// nouns in the examples: an insurance reader would anchor every answer.
//
// The second paragraph exists because the bans let literals through: field
// names and property keys in running text, and never the figure the reader
// wanted (the hold plus the hourly job, as one range). answerStages is
// appended after this block and keeps its "copied character for character"
// rule on purpose - the literal is the anti-hallucination check - so this
// paragraph keeps the literal too and only moves it: once, after the words,
// out of the lead.
//
// The flowchart sentence exists because this block shaped the answer as
// prose-and-stop, and the diagram rule appended at the end of the prompt
// never overrode it: Analyst answers to process questions came back as
// paragraphs alone. The trigger is said in the reader's words - a case, an
// approval, a hand-off -
// because "control flow" and "call sequence" in the diagram rule never
// fired on a business question. It names the picture, never the fence:
// the shape rules must precede the first fence in the prompt (answer_test).
const answerBA = `
Audience: business analyst. The person who will read this answer decides what
the system is supposed to do and checks whether it does it. They know the
business inside out: the actors, the rules, the vocabulary, what the numbers
mean and which systems the business runs on. Write in that vocabulary and
spend the words on the mechanism; where the code gives a business term a
meaning of its own, that meaning is part of the answer. The software itself is
new to them: say what each system does for the business, and where the answer
would turn on how the code is written, how the systems reach each other, how
the data is shaped or how any of it is deployed, state the effect the business
sees. What they want is the rule as a rule: what triggers it, what then
happens, after how long, and under which conditions it applies or not - a
condition that decides whether the rule applies is part of the rule; a rare
failure is an edge case and belongs in a follow-up. The opening sentence
carries the one of these the question asked for; the others follow in the
paragraphs. Where the sources show it, say whether the behaviour is fixed in
the code, set by configuration, or a manual step, because that decides whether
a change is a configuration ticket or a development story; where the sources
do not show it, say nothing about it. Explain the mechanism in three to five
paragraphs in the language of the business domain. Where the mechanism is a
process the reader follows step by step - a case that moves through states,
an approval, a hand-off with a decision on the way - the answer also carries
a flowchart of it; where it is an exchange between systems, roles or people,
a sequence diagram. The diagram sits after the paragraph that introduces the
steps. No source code, no signatures, no file paths in running text. Answer
the question, draw the flow if there is one, and then stop.

Identifiers, property keys, cron expressions and code defaults are not the
language of the business domain. Say what a value means for the reader: a
schedule in words ("hourly, on the hour"), a delay as its duration, a switch as
what it turns on. The exact value still appears, copied character for
character, once, in parentheses after the words that explain it - never on its
own, never in the opening sentence. When two settings combine, state the
outcome the reader experiences as one figure or range, not the settings
separately.`

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
into a single mechanism they do not share. Where the repositories call each
other and no process walk is listed below, the diagram is a sequence between
them. Repository names stay as they are.`

// answerCompareProjects is answerCompare for a turn comparing PRODUCTS rather
// than bare repositories. Separate text rather than a shared template with the
// noun substituted: these are the rules a wrong answer comes from, and a
// sentence assembled from fragments is the one kind nobody proof-reads.
//
// It says a project may span several repositories, because the sources will
// carry repository names the question never used, and "attribute every claim to
// the repository it came from" alone would read as an instruction to compare
// those instead of the products the reader asked about.
const answerCompareProjects = `

The question names these projects: %s. Each is a separate product with its own
implementation and all of them are in the sources. A project may span several
repositories: treat everything from one project as one system. Cover every
project, say plainly where they differ and where they agree, and attribute every
claim to the project it came from as well as to the source it rests on. Do not
answer for one and leave the others out; do not merge them into a single
mechanism they do not share. Where the projects call each other and no
process walk is listed below, the diagram is a sequence between them. Names
stay as they are.`

// answerMissingRepo is added when the question named a repository the index
// does not carry. Without it the model is handed "how do loom and rongo
// differ" plus rongo-only sources, and writes loom's side from its own
// training — the invention the whole prompt is built to prevent.
const answerMissingRepo = `

The question names repositories that are NOT indexed: %s. There are no sources
for them and you know nothing about them. Say in one sentence that they are not
in the index, answer for the rest, and make no claim of any kind about their
code - not a guess, not a comparison, not "presumably".`

// answerAllDenied is added when the question asked for every repository and
// the thread is narrowed to some. Its one format argument is what the thread
// carries.
//
// Without it the model holds a question explicitly asking for a comparison
// across the corpus and sources from one thread's worth of it, which is the
// position answerMissingRepo guards against with a name in it and this one
// without.
const answerAllDenied = `

The question asks about every project. Only %s is in front of you, and this
thread covers nothing else. Say in one sentence that the answer is for those
alone and that a new thread can answer across the whole corpus, then answer for
them. Make no claim of any kind about any other repository - not a guess, not a
comparison, not "presumably".`

// answerFollowUp is added when the turn continues a thread that already
// answered something. Its one format argument is the PREVIOUS QUESTION.
//
// The previous answer's text is not here and must not be: the sources are what
// a claim rests on, and a model handed its own earlier prose alongside sources
// it was NOT written from ends up restating it and citing the new sources for
// it. The question is enough for both things this rule is for — telling the
// model what a pronoun points at, and telling it not to write the same answer
// again with a picture on top. A rework is the exception, and it has its own
// block: answerRework.
const answerFollowUp = `

This is a follow-up to an earlier question in the same thread: %s. Answer the
NEW question. Where it points at something without naming it ("that", "this",
"it"), it points at the subject of that earlier question. Do not restate what
was already explained - the reader has it directly above.`

// answerRework replaces answerFollowUp on a turn that asks for the previous
// answer in another form. Its one format argument is the reader's
// instruction. The previous text stands in the user message beside the
// sources it was written from, so the rule can ask for both: keep every
// claim on its source, and add nothing the text did not already say.
const answerRework = `

This turn does not answer a new question. The reader asked for the previous
answer, given in full under "Previous answer", to be written again in another
form: %s. Follow that instruction on the shape and length of the text, even
where it departs from the shape rules above. Keep every claim on the source it
rests on and cite it by its number from the list. Add nothing the previous
answer does not say, and drop a claim only where the instruction asks for less.
Where the instruction asks for less and a claim has to go, drop it whole,
never its citation alone. A summary, or "shorter", is at most half the length
of the previous answer, and a diagram in it is kept only when the instruction
asks for one.`

// answerOutsideRepo is added when the question named a repository this THREAD
// does not carry. The model's position is the same one answerMissingRepo
// guards: named on one side, no sources on the other, and nothing but training
// to fill the gap with. The reason differs and so does the sentence — the
// repository is indexed and rongo could answer about it, just not in this
// thread — but the rule is identical, because an invented comparison is
// invented either way.
const answerOutsideRepo = `

The question names repositories this thread does not cover: %s. There are no
sources for them here and you know nothing about them. Say in one sentence that
this thread does not cover them and that a new thread can, answer for the rest,
and make no claim of any kind about their code - not a guess, not a comparison,
not "presumably".`

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

// answerStages is added when a source lies under a declared stage directory
// of an infrastructure repository. Without it the model reads three
// properties files as three claims about one value and picks one; with it
// the code's placeholder and default are the default and each stage file is
// the value deployed there, which is the shape the question has.
const answerStages = `

Sources marked "(stage X)" are deployed configuration: a file under a stage
directory of an infrastructure repository holds the value that stage runs
with. The code's placeholder ("${key:default}") and the repository's own
default properties are the DEFAULT, which a stage file overrides. Answer with
the default first, then the value for every stage by its name, each from its
own source, and say when two stages share a value rather than reporting one
value as the value. Every value is copied character for character from the
line that sets it - a cron expression is quoted as written, never
paraphrased or reassembled from memory.`

// answerStageAsked replaces answerStages' last sentence when the question
// named a stage: the search and the crossing were narrowed to it, so the
// sources hold that stage alone, and the answer has to say so rather than
// present its value as the value everywhere.
const answerStageAsked = `

Sources marked "(stage X)" are deployed configuration: a file under a stage
directory of an infrastructure repository holds the value that stage runs
with. The code's placeholder ("${key:default}") and the repository's own
default properties are the DEFAULT, which a stage file overrides. The
question was asked for the stage %s and the sources were narrowed to it:
answer with the default and that stage's value, name the stage, and say in
one sentence that the other stages were not looked at. Every value is copied
character for character from the line that sets it - a cron expression is
quoted as written, never paraphrased or reassembled from memory.`

// answerProcesses carries the wiring of the process models among the sources.
// It exists because a model's nodes and flows reach the prompt as separate
// chunks, in retrieval order, and a list of sourceRef/targetRef pairs is not
// something the answer orders into a walk: measured, every trace question was
// answered as a paragraph per chunk with no sequence. The listing gives the
// order and the branching; the claims still come from the sources, which is
// why the block ends with the same rule the structure block does.
//
// The last sentence asks for the walk as the diagram: the listing is already
// order plus branch conditions, which is a flowchart in text, and it is the
// strongest signal the prompt has that the answer is a process. It wins over
// the sequence the comparison blocks ask for, and they say so, because both
// can land in one prompt under "at most one diagram".
// answerLocated carries what the locate loop concluded after reading what it
// found. It is a pointer into the sources, so the rules on it are the rules on
// a pointer: follow it, cite the source it names, and if the sources do not
// bear it out, the sources win. The loop read excerpts and may have been
// looking at the wrong one; the numbered sources are what the answer stands on.
// A "not found" from it never overrides a source that answers: the loop runs
// last, over sources the walk already gathered, so a false negative is its
// expected failure shape.
const answerLocated = `

Before this answer was written, the question was traced through the index by
searching and reading, and that search concluded:

%s
Treat this as a POINTER to the place among the sources that matters, not as
the answer and not as a source. Find the source it names, read it, and write
from THAT - the citation is to the source, never to this note. Where the
sources do not bear it out, the sources are right and this note is wrong: it
was written from excerpts and may have landed on a near miss. It is the
weakest evidence in front of you. If it says the place was not found, that is
its expected failure, not a finding: it read clipped excerpts after the
sources were gathered. A source that answers the question wins, and the
answer is written from it.

When the note names a place and a source bears it out, the sources have been
ordered to put it first. Open the answer with that place: the file, the line
and the code there, cited to the source holding it, in the opening sentence.
Then explain the rest.`

const answerProcesses = `

The process models among the sources are wired as follows, read from the model
files themselves, one line per node in walk order from the start event, each with the nodes it
leads to and the condition on each branch:

%s
Use this for the ORDER of the steps and for which branch is taken when: it is
what the model file declares. What a step does is described in the sources and
cited from them - cite the node's own source, never this listing. A called
process listed here whose nodes have no source of their own may be walked by
the names above, and then say in a clause that its model was not among the
sources; say nothing about what its steps do inside. This walk is the
flowchart: draw it as the answer's diagram, one node per step above, the
branch condition as the edge label, the steps' sources cited in the sentence
that introduces it.`

// answerLinks is the link census block. The listing is read from the index,
// so it is complete where the sources are capped; what the sites lead to is
// in the sources, and a site written on a variable is resolved from the
// configuration source the walk pulled in, never guessed.
const answerLinks = `

Every place the repository navigates from, read from the index, one line per
target as "target  repository/path:line, ..." with the places it is written
at. The target is the text at the site: a URL, or the expression the code
builds it from:

%s
The listing names every site the index holds, except a tail it counts
instead of naming; the sources hold the code at the sites that fit and the
configuration the expressions read. Group the sites by where they lead, say
which lead to another application and which stay inside this one, and cite
the source at each site - never this listing. A target built from a variable
is resolved from the configuration among the sources; when no source sets it,
say so rather than guessing the host.`

const answerDev = `
Audience: developer. Name types, functions and files, and quote short excerpts
where they carry the explanation. A fenced code block carries its language tag
(` + "```go" + `, ` + "```typescript" + `), never a bare ` + "```" + `. Describe
the control flow so that it can be followed in the code.`

// answerShape follows the audience block for BOTH audiences, which is why it
// is its own constant rather than a paragraph in each of them.
//
// It exists because neither audience block said anything about shape. They
// ask for three to five paragraphs and for the control flow to be followable,
// and the model does the safe thing with that: undifferentiated prose, with
// the sentence that actually answers the question somewhere in the middle of
// the first one. The renderer has carried headings, lists and bold since it
// was written (index.css) and nothing was ever emitted into them.
//
// The lead sentence is the half that matters. It also makes understand.go's
// answerRecall = 1200 more true than it was: that constant is justified by
// "the opening carries the subject", and now it does by instruction.
//
// The list rule is fenced on both sides on purpose. "Structure it" alone comes
// back as an answer bulleted into fragments, which is a different way of being
// unreadable - so the permission names what a list is FOR (a set the code
// really has) and says twice what it is not for.
//
// The list permission names "ordered steps, branches, conditions", which is
// the material a flowchart is made of, so without the last sentence the list
// rule won by default and the diagram rule at the end of the prompt found
// nothing left to draw. The line is drawn where the diagram rule draws it: a
// branch or a second party.
//
// No headings: they were mocked up and deliberately left out. A short answer
// wearing three ### headings looks over-built, and that judgement is one the
// model gets wrong more often than it gets the list wrong. If dev answers
// still read long with this in, headings are the next thing to try.
const answerShape = `

Open with ONE sentence that answers the question, then explain. A reader who
stops after that sentence has the answer; a reader who goes on gets why.

Where the mechanism really is a set - branches, options, ordered steps,
conditions - carry it as a short list instead of a paragraph. Prose that is
prose stays prose: never split a single line of reasoning into bullets, and
never use a list where two sentences would do. Ordered steps with a branch or
a second party are a diagram, not a list (the diagram rule below); a plain
set stays a list. Markers sit on the list item that makes the claim, exactly
as they do in running text.`

// answerIntent is the one-line refinement of the shape rule per intent, keyed
// by Understanding.Intent and appended directly after answerShape.
//
// The shape rule says to open with ONE sentence that answers the question. On
// its own that leaves the model to decide what answering means, and for three
// of the four intents it decides wrong: a WHERE question comes back with one
// place explained in depth and the other two mentioned in passing, a WHY
// question comes back with the mechanism and never the rule that decides it,
// and a yes/no question comes back with an explanation the reader has to grade
// themselves.
//
// "how" is the intent the shape rule was written for, so it adds nothing, and
// an unknown value adds nothing either: the intent comes from a model, and a
// word this map does not carry must leave the prompt exactly as it was.
//
// The WHERE rule says "place" and "marker", never "file": an Analyst answer
// carries no paths in running text (answerBA), and a rule naming files would
// pull them back in.
var answerIntent = map[string]string{
	"where": "\n\nThe question asks WHERE: open by saying every place the mechanism lives, " +
		"each with its marker, before explaining any of them.",
	"why": "\n\nThe question asks WHY: state the condition or rule that decides it and the " +
		"reason the code gives for it, then the mechanism.",
	"conformance": "\n\nThe question asks whether the code does something: answer yes or no in " +
		"the first sentence, then show what decides it.",
}

// answerDiagram follows the audience block, so "the audience rules above"
// are the ones the model just read: a Developer diagram names functions and
// files, an Analyst diagram speaks the domain. The fence is named literally,
// as the DEV block names ` + "```go" + `: the model needs the syntax, not a
// description of it.
//
// The trigger is positive and audience-neutral on purpose. It used to read
// "only where control flow or a call sequence carries the explanation",
// which is a restriction in developer words: an Analyst question is about a
// process, a hand-off, a decision, and the words never fired. Saying what
// earns a picture and what does not (a plain sequence of steps is a list, one
// rule or value is prose) draws the line the shape rule's list permission
// otherwise claims for itself.
//
// The type table is the reason the renderer changed. With two shapes on
// offer, a question whose answer is a mapping - which trigger produces to
// which topic, which handler serves which route - came back as a flowchart
// with an invented order between the targets and one target drawn twice
// (share W5L1FYhvOF, 2026-09-17). Each row names the shape of the content
// and the type that draws it; the mapping row says outright that a target is
// one node, because that is the error the flowchart made.
//
// The picture does not cite. It used to, through a src array per node, and
// the chips were the reason the renderer was hand-written; the sentence that
// introduces the diagram carries the markers now, as a doc-only claim
// already does. Inside the fence a bracket is syntax, so a marker there
// breaks the picture, and the rule says so.
//
// The label rule exists because with nothing said about length the model
// wrote sentences ("RANK 1: gefundene Code-Fragmente nach Relevanz zur Frage
// bewerten") and the reader got boxes of half-sentences, seen 2026-09-16.
// Quotes around every label are what keeps a label with a parenthesis, a
// colon or an umlaut from being read as syntax: the renderer's parser is
// strict and a parse error loses the whole picture.
//
// The closing paragraph settles a contradiction the model was left to resolve
// on its own: answerBA bars source code from an Analyst answer, and a diagram
// is syntactically a code fence, so the block came back tagged as something
// else or with no fence at all and the reader got text.
const answerDiagram = `

At most one diagram. Draw one when the explanation is steps in an order with
a decision that splits the path; two or more parties exchanging messages in
an order, whether the parties are functions and services or
roles, systems and people; a set of states with the transitions between
them; entities with the relations between them; or a mapping from triggers
to targets.
Steps with no branch and one party are a list, not a diagram. Skip it when
the answer is one rule, one value or one place.
The diagram is a fenced block tagged ` + "```mermaid" + ` holding mermaid
syntax, and its type follows the shape of the content:
- steps with a branch: flowchart TD, a decision as a {"?"} node, the
  condition as the edge label
- an exchange between parties over time: sequenceDiagram, one participant
  per party
- states and transitions: stateDiagram-v2, the guard on the transition
- entities and their relations: erDiagram, with the fields that matter
- a mapping from triggers to targets (which event produces to which topic,
  which handler serves which route): flowchart LR with two subgraphs, the
  triggers in one and the targets in the other, an edge per pair and the
  condition as its label. A target that several triggers reach is ONE node
  with several edges into it, never drawn twice, and there is no edge between
  two targets: a mapping has no order.
At most 12 nodes, 5 participants, 12 messages. Every node label sits in
double quotes - a["Treffer bewerten"], b{"Zeiträume vorhanden?"} - because
a parenthesis, a colon or a bracket outside quotes is syntax and breaks the
picture; a sequence message after the colon needs no quotes. A label is a
title, not a sentence: a noun phrase or an imperative of at most five words,
about 40 characters - "Treffer bewerten", not "RANK 1: gefundene
Code-Fragmente nach Relevanz zur Frage bewerten". No step numbers, no
"RANK 1:" prefixes, no clauses. Labels follow the audience rules above and
are written in the answer language; ids stay short ASCII without spaces.
What a node cannot say in five words goes in the prose.
No citation markers inside the fence: the sentence that introduces the
diagram carries the markers for what it shows, and the prose still explains;
the diagram is not a substitute.

The block is a diagram, not source code: an audience rule that bars code,
signatures or file paths from running text does not bar it, and it is written
for every audience. Open it with ` + "```mermaid" + ` and nothing else - not
` + "```json" + `, not ` + "```diagram" + `, and never without a fence. A
block opened any other way is printed as text and the reader gets no
picture.`

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
	// Outside are repositories the question named that the THREAD does not
	// carry — named, indexed, and still not searched, because a turn in this
	// thread already narrowed to something else and a thread only ever
	// narrows. Distinct from Unknown, which is a name the index does not have
	// at all: saying "not indexed" about a repository rongo indexes would be
	// a lie, and saying nothing would let the answer look like it covered the
	// repository that was asked about.
	Outside []string `json:"outside,omitempty"`
	// AllDenied is the question having asked for every repository inside a
	// thread that is narrowed to some. The narrowing wins — a thread does not
	// widen — but the reader asked for the whole corpus and is getting one
	// thread's worth, and that difference has to be said out loud for the same
	// reason Outside does. It is the "all repositories" case of Outside, which
	// cannot be expressed there because the question named no repository at
	// all.
	AllDenied bool `json:"all_denied,omitempty"`
	// All is the reader asking for the whole corpus on purpose — either the
	// question said so, or they picked "all repositories" off a repository
	// card. It is what tells the repository rung in Decide that a turn
	// spanning several repositories is wanted rather than ambiguous, and it is
	// part of the record: a resumed or re-explained turn must answer under the
	// same permission the first one had.
	All bool `json:"All,omitempty"`
	// Intent is what the understanding step read the question as asking for:
	// how, why, where or conformance. Empty is "nothing said", and so is any
	// value the answer prompt has no rule for.
	//
	// It reaches the answer prompt, which is the only place it changes an
	// answer: the shape rules say to open with one sentence that answers the
	// question, and what "answers the question" means is different for each
	// of them. Part of the record for the reason Stage is — a resumed or
	// re-explained turn answers the question that was asked, not a different
	// reading of it.
	//
	// The column is a JSON blob, so a row written before this field decodes
	// to the empty string and the prompt is the one it was written under.
	Intent string `json:"intent,omitempty"`
	// SinceDays is the window a changes turn answered for, in days, after
	// the default was applied; zero on every other intent. Part of the
	// record for the reason Stage is: a re-explained turn describes the
	// same days.
	SinceDays int `json:"since_days,omitempty"`
	// Topic is what the changes turn was narrowed to, or empty for the
	// whole window. Recorded beside SinceDays so the prompt can say it.
	Topic string `json:"topic,omitempty"`
	// Between is the two stages a release turn compared, and Release one
	// line per image the infrastructure repository deploys under them, as
	// the turn resolved it. Part of the record for the reason SinceDays
	// is: a re-explained turn describes the same ranges, and the share page
	// shows what was compared.
	Between []string      `json:"between,omitempty"`
	Release []ReleaseLine `json:"release,omitempty"`
	// DocsOnly is true when every source the answer was written from is
	// documentation. Not something the question said, but it belongs here for
	// the same reason Unknown does: it is what the turn has to tell the reader
	// about its own footing, and it has to survive into the record so a resume
	// and a re-explain rebuild the same notice and the same prompt rule.
	//
	// The column is a JSON blob, so an older row simply decodes to false and
	// no migration is needed.
	DocsOnly bool `json:"docs_only,omitempty"`
	// Projects are the projects Known covers ENTIRELY, sorted.
	//
	// It exists because len(Known) >= 2 has always meant "comparison", in three
	// independent places, and a project expands to its members. Without this a
	// question about one product would be answered as a comparison of its own
	// backend against its own UI, under a prompt rule telling the model to say
	// plainly where they differ.
	//
	// Entirely is the point. A reader who named one repository of a three-repo
	// project asked about that repository, so a partial cover counts for
	// nothing and the turn behaves exactly as it did before projects existed.
	Projects []string `json:"projects,omitempty"`
	// Loose are the repositories in Known that no fully covered project
	// accounts for: a second product the reader named only part of.
	//
	// It is what keeps the project rule from swallowing a real comparison.
	// "How does shop differ from loom-core" covers shop whole and loom not at
	// all, so Projects holds one name while the sources in front of the model
	// come from two products. Answered as one product it would be answered as
	// one mechanism, with nothing telling the model to cover both sides or to
	// say which claim came from which. With anything loose the turn falls back
	// to the repository-grained comparison it had before projects existed.
	//
	// Derived per turn like Structure, and never persisted for the same
	// reason: it is a fact about the current repos.yaml, not about the turn.
	Loose []string `json:"-"`
	// Structure is the project structure block for the answer prompt: which
	// repository plays which part, and which calls which.
	//
	// Not persisted, and derived per turn from the live repos.yaml — the same
	// choice Message.Notice makes and for the same reason. A re-explained turn
	// therefore describes the structure as it is now rather than as it was,
	// which is what a re-index does to sources too, and better than a second
	// stored blob that can rot.
	Structure string `json:"-"`
	// Stage is the deployment stage the question asked about, as a declared
	// stage name, or empty. Part of the record: a resume or re-explain
	// searches and labels under the same stage the first turn did. Per turn,
	// never inherited from the thread — a stage is a view of the same
	// subject, and "and on intg?" is a follow-up, not a widening.
	Stage string `json:"stage,omitempty"`
	// Stages are the declared stages of the live repos.yaml, for labelling a
	// source under a stage directory and narrowing the crossing. Derived per
	// turn like Structure, never persisted, for the same reason.
	Stages stages.Set `json:"-"`
	// Processes is the wiring of the process models among the sources, for
	// the answer prompt: one line per node, the branches with their
	// conditions, the models the call activities run. Derived per turn from
	// the model files at their indexed commit, never persisted, for the
	// reason Structure is not.
	Processes string `json:"-"`
	// Census is the listing the understanding step read the question as
	// asking for — CensusLink, or empty for a mechanism question. Part of
	// the record for the reason Intent is: a resumed turn reads the same
	// listing the first one would have. An older row decodes to none.
	Census string `json:"census,omitempty"`
	// Links is the link census listing for the answer prompt: every
	// navigation site of the named repositories with its place, read from
	// the index at answer time. Derived per turn like Processes, never
	// persisted, for the same reason.
	Links string `json:"-"`
	// Located is what the locate loop concluded after reading what it found:
	// the file, the line and the code that does the thing asked about. Empty
	// when the loop is off or looked at nothing.
	//
	// A POINTER into the sources, never a substitute for them. The answer is
	// still written from the numbered sources and every claim still cites
	// one; this says which of ninety is the one that matters, which is what
	// something that just read the code knows and a cold answer call does
	// not. Derived per turn, never persisted, like Processes.
	//
	// So a re-explain answers WITHOUT it: the loop does not run again, and
	// storing the sentence would keep model prose about code, which the
	// index and the record never hold. The re-explained answer stands on the
	// same sources and may point at a different one.
	Located string `json:"-"`
	// Resumed is this turn continuing a clarification card rather than
	// answering a fresh question. It stops the locate loop: a module card
	// replays exactly the hits its candidate was built from, and a loop that
	// searched past them would answer from code the card never offered.
	//
	// Per turn, never persisted: the stored scope belongs to the turn that
	// asked, and a re-explain of it is not itself a resume.
	Resumed bool `json:"-"`
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
		// A commit is neither code nor prose about it; a changes turn is
		// never "documentation only".
		if s.IsCommit() || !retrieve.IsDocPath(s.Path) {
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
	LanguageEN: "No project called %s in the index. Answered for %s alone.",
	LanguageDE: "Kein Projekt namens %s im Index. Nur %s beantwortet.",
	LanguageFR: "Aucun projet nommé %s dans l'index. Réponse portant sur %s uniquement.",
	LanguageIT: "Nessun progetto di nome %s nell'indice. Risposta solo su %s.",
}

// scopeNoticeWhole is the same sentence when the question named nothing the
// index carries: there is no narrowed scope to name, so the turn searched
// everything.
var scopeNoticeWhole = map[Language]string{
	LanguageEN: "No project called %s in the index. Searched every indexed project.",
	LanguageDE: "Kein Projekt namens %s im Index. Alle indexierten Projekte durchsucht.",
	LanguageFR: "Aucun projet nommé %s dans l'index. Recherche sur tous les projets indexés.",
	LanguageIT: "Nessun progetto di nome %s nell'indice. Cercato in tutti i progetti indicizzati.",
}

// outsideNotice is the "this thread is narrowed, and the repository you just
// named is not in it" sentence. Templated for the same reason scopeNotice is.
//
// It names the way out, because there is one and it is not obvious: a thread
// only ever narrows, so the repository that was asked about is reachable from
// a new thread and from nowhere else. Without the second half the reader is
// told what did not happen and given nothing to do about it.
//
// Two format arguments: the repositories the thread carries, then the ones it
// does not.
var outsideNotice = map[Language]string{
	LanguageEN: "This thread is narrowed to %s. %s was not searched. Open a new thread for it.",
	LanguageDE: "Dieser Thread ist auf %s eingegrenzt. %s wurde nicht durchsucht. Dafür einen neuen Thread öffnen.",
	LanguageFR: "Ce fil est restreint à %s. %s n'a pas été consulté. Ouvrez un nouveau fil pour cela.",
	LanguageIT: "Questo thread è ristretto a %s. %s non è stato cercato. Apri un nuovo thread per quello.",
}

// allDeniedNotice is outsideNotice's other half: the question asked for every
// repository, so there is no name to put in the second slot — the thread's own
// repositories are the only thing to name, and the way out is the same one.
//
// One format argument: the repositories the thread carries.
var allDeniedNotice = map[Language]string{
	LanguageEN: "This thread is narrowed to %s. It cannot answer across every project. Open a new thread for that.",
	LanguageDE: "Dieser Thread ist auf %s eingegrenzt. Über alle Projekte hinweg kann er nicht antworten. Dafür einen neuen Thread öffnen.",
	LanguageFR: "Ce fil est restreint à %s. Il ne peut pas répondre sur l'ensemble des projets. Ouvrez un nouveau fil pour cela.",
	LanguageIT: "Questo thread è ristretto a %s. Non può rispondere su tutti i progetti. Apri un nuovo thread per quello.",
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
// footing: a repository the index does not carry, a repository this thread has
// already narrowed away, an answer that stood on documentation alone, or "" when
// none of them applies — which is the ordinary case, and says nothing.
//
// It takes the whole Scope rather than its parts so that every caller —
// the pipeline, a resume and a re-explain — renders from one value and none of
// them has to be found again when the Scope grows a field.
//
// Several sentences applying is not a contradiction and all are said, scope
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
	if len(sc.Outside) > 0 && len(sc.Known) > 0 {
		parts = append(parts, fmt.Sprintf(outsideNotice[l],
			strings.Join(sc.Known, ", "), strings.Join(sc.Outside, ", ")))
	}
	if sc.AllDenied && len(sc.Known) > 0 {
		parts = append(parts, fmt.Sprintf(allDeniedNotice[l], strings.Join(sc.Known, ", ")))
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
	LanguageEN: "All projects",
	LanguageDE: "Alle Projekte",
	LanguageFR: "Tous les projets",
	LanguageIT: "Tutti i progetti",
}

var allReposSummary = map[Language]string{
	LanguageEN: "Answer across every indexed project.",
	LanguageDE: "Über alle indexierten Projekte hinweg antworten.",
	LanguageFR: "Répondre sur l'ensemble des projets indexés.",
	LanguageIT: "Rispondere su tutti i progetti indicizzati.",
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

// StructureBlock renders what a project is made of, for the answer prompt.
//
// Templated from repos.yaml, never written by a model, and never a source. It
// is the same class of input as a manifest: it decides what the model is told
// about the shape of the code, not what the answer may claim to have read. The
// closing sentence is the rule that keeps it that way, in the shape the docs
// rule and the unindexed-repository rule already use.
//
// It exists for one case above all: a project with two backends, where "part:
// backend" is true of both and only the declared edge says which one the
// storefront calls. The NEGATIVE half carries as much as the positive — a model
// reading a fetch() in the UI beside two plausible APIs will otherwise pick one.
//
// A project with nothing declared and one member produces nothing at all.
// Absence takes no marker, and "rongo is one product in 1 repository" tells the
// model something it can already see in every citation.
//
// A library the product is built on is a member like the rest — searched
// with the product, drawn in its wiring — and its line says it is shared
// with other products, so the model does not read its code as this product's
// own. A uses edge to it is an ordinary connection inside the project. A
// uses edge to anything else outside the project cannot occur: projects.Load
// drops every such row, so an unknown target here is skipped.
func StructureBlock(ps []projects.Project) string {
	var b strings.Builder
	for _, p := range ps {
		var edges []string
		var unreached []string
		reached := map[string]bool{}
		member := make(map[string]bool, len(p.Members))
		for _, m := range p.Members {
			member[m.Name] = true
		}
		declared := len(p.Members) > 1
		for _, m := range p.Members {
			if m.Part != "" || m.Description != "" || len(m.Uses) > 0 {
				declared = true
			}
			for _, u := range m.Uses {
				if !member[u] {
					continue
				}
				edges = append(edges, m.Name+" uses "+u+".")
				reached[u] = true
			}
		}
		if !declared {
			continue
		}
		// A library's own block, when a turn reaches it: it is a project of
		// one in the map, and calling it a product here right after another
		// block said it is a shared library would name one repository as two
		// things in one prompt.
		if len(p.Members) == 1 && p.Members[0].Library {
			fmt.Fprintf(&b, "\n\nRepository %q is a shared library, used by other products and indexed on its own.\n\n", p.Name)
		} else {
			fmt.Fprintf(&b, "\n\nProject %q is one product in %d repositories.\n\n", p.Name, len(p.Members))
		}
		for _, m := range p.Members {
			fmt.Fprintf(&b, "  %s", m.Name)
			if m.Library && len(p.Members) > 1 {
				b.WriteString(" (shared library)")
			} else if m.Part != "" {
				fmt.Fprintf(&b, " (%s)", m.Part)
			}
			if m.Description != "" {
				fmt.Fprintf(&b, " — %s", m.Description)
			}
			b.WriteString("\n")
		}
		// Only when there is an edge to contrast with. With no edges at all,
		// "nothing uses any of them" is noise, and the useful fact — that these
		// repositories are one product — has already been said.
		if len(edges) > 0 {
			b.WriteString("\nDeclared connections inside the project:\n")
			for _, e := range edges {
				fmt.Fprintf(&b, "  %s\n", e)
			}
			for _, m := range p.Members {
				if !reached[m.Name] {
					unreached = append(unreached, m.Name)
				}
			}
			if len(unreached) > 0 {
				fmt.Fprintf(&b, "Nothing in this project uses %s. They are reached from outside it, "+
					"so do not connect them to the others yourself.\n", strings.Join(unreached, ", "))
			}
		}
	}
	if b.Len() > 0 {
		b.WriteString(structureIsConfiguration)
	}
	return b.String()
}

// structureIsConfiguration closes a structure block, once, last: the project
// blocks and the units paragraphs are one class of input and one rule
// covers them. describeProjects re-closes the block after the paragraphs
// with this same constant.
//
// The last sentence settles which of the two inputs wins on WHICH PARTS EXIST.
// The block is declared and complete; the sources are a retrieval cut and are
// almost never complete. Without the sentence the model reads the gathered
// code as the inventory and reports the parts it cannot see as absent or not
// indexed, which is a false claim about the corpus made from a partial view of
// it. It does not license a claim ABOUT those parts: their code is still not
// in front of the model, and "never invent" is what covers that.
const structureIsConfiguration = "\nThis is configuration, not code. It says which repository plays which part " +
	"and which calls which. Never present it as something you read in the sources, and never cite it." +
	" When a source shows only some of the parts this block lists, the block is complete and the source is " +
	"partial: say the parts exist and that their code is not among the sources, never that they are absent " +
	"or not indexed."

// Answer writes the answer for one turn, streaming it token by token.
//
// With nothing gathered it returns the "nothing found" answer WITHOUT calling
// the model. A model handed only a question and a system prompt answers it
// fluently from its own training, and that answer would be about some other
// codebase — the single most expensive failure this product can produce.
// followingUp is the question this thread asked last, empty when there is
// none. Only the question: see answerFollowUp for why the previous answer's
// text stays out of here.
func (a *Answerer) Answer(ctx context.Context, question string, audience Audience, lang Language,
	sources []Source, scope Scope, followingUp string, onToken func(string)) (Answer, error) {

	if len(sources) == 0 {
		return Answer{Text: NothingFound(lang, nil)}, nil
	}
	rules := memory.Applying(memory.From(ctx).Rows(), scope.Known)
	system := systemPrompt(audience, lang, sources, scope, followingUp, "", memory.Block(rules, scope.Known))
	user := renderSources(question, sources, scope.Stages)
	// Measured before the call, from the exact strings that go on the wire.
	// The user message is the question with the code under it, so the code is
	// what is left once the question is taken off — counted that way round
	// because the headers, paths and separators renderSources writes are part
	// of what the sources cost, and attributing them to the question would
	// flatter the figure that matters.
	asked := estimateTokens(question)
	parts := PromptParts{
		System:   estimateTokens(system),
		Sources:  max(estimateTokens(user)-asked, 0),
		Question: asked,
	}
	answer, err := a.stream(ctx, lang, sources, system, user, parts, onToken)
	answer.Memories = len(rules)
	return answer, err
}

// Rework writes the previous answer again in the form the instruction asks
// for, from that answer's own sources. The previous text goes into the user
// message beside them — the one place in the product where prose of the
// model's own reaches the answering prompt, and it is safe here because the
// sources next to it are the ones it was written from.
//
// No follow-up rule: "do not restate what was already explained" would forbid
// the one thing this call exists to do, the same as Reexplain.
func (a *Answerer) Rework(ctx context.Context, instruction string, audience Audience, lang Language,
	t Thread, scope Scope, onToken func(string)) (Answer, error) {

	if len(t.Sources) == 0 {
		return Answer{}, fmt.Errorf("rework: no sources to rework from")
	}
	rules := memory.Applying(memory.From(ctx).Rows(), scope.Known)
	system := systemPrompt(audience, lang, t.Sources, scope, "", fmt.Sprintf(answerRework, instruction), memory.Block(rules, scope.Known))
	user := renderRework(instruction, t, scope.Stages)
	// The sources are measured on their own here: the user message also
	// carries the previous turn, which is neither the question nor the code
	// and is left out of the split. The billed total still covers it.
	var list strings.Builder
	renderSourceList(&list, t.Sources, scope.Stages)
	parts := PromptParts{
		System:   estimateTokens(system),
		Sources:  estimateTokens(list.String()),
		Question: estimateTokens(instruction),
	}
	answer, err := a.stream(ctx, lang, t.Sources, system, user, parts, onToken)
	answer.Memories = len(rules)
	return answer, err
}

// systemPrompt assembles the answering rules for one turn. followingUp is
// the previous question of an ordinary follow-up, rework the rendered rework
// block; at most one of them is set. memories is the reader's standing
// instructions as memory.Block rendered them, empty for a reader with none,
// and then the prompt is byte for byte what it was before memory existed.
func systemPrompt(audience Audience, lang Language, sources []Source, scope Scope, followingUp, rework, memories string) string {
	name := languageName(lang)
	system := fmt.Sprintf(answerCommon, name)
	if audience == AudienceDev {
		system += answerDev
	} else {
		system += answerBA
	}
	// Directly after the audience block and before everything conditional: the
	// shape rules are about the whole answer, so they must not read as though
	// they applied only to the last special case that happened to be appended.
	system += answerShape
	// Directly after the shape rules it refines: what "one sentence that
	// answers the question" means depends on what the question asked for.
	// A missing or unknown intent adds nothing.
	system += answerIntent[scope.Intent]
	system += changesBlock(scope, audience)
	system += releaseBlock(scope, audience)
	// After the audience block, so "cover every one of them" is read against
	// the shape the audience block just set rather than before it.
	//
	// Against the repositories the SOURCES actually cover, never against the
	// names alone: a named repository can be indexed, be searched on its own,
	// and still return nothing for this question. Telling the model to cover
	// it anyway is an instruction to invent, which is the one thing the rest
	// of this prompt exists to prevent.
	//
	// A turn covering ONE project whole is not a comparison, whatever its
	// repository count: a backend and its UI are one product, and telling the
	// model to say where they differ would answer the wrong question. Two or
	// more projects still compare, and the rule names those rather than the
	// repositories underneath, because products are what the reader asked
	// about.
	//
	// Both project branches need Known to hold nothing BUT those projects.
	// One project covered whole beside half of another is two products in
	// front of the model, and naming only the covered one would tell it to
	// cover a set smaller than what it was given.
	switch {
	case len(scope.Loose) > 0:
		if covered := coveredRepos(scope.Known, sources); len(covered) >= 2 {
			system += fmt.Sprintf(answerCompare, strings.Join(covered, ", "))
		}
	case len(scope.Projects) == 1:
		// Nothing: one product, answered as one mechanism.
	case len(scope.Projects) >= 2:
		system += fmt.Sprintf(answerCompareProjects, strings.Join(scope.Projects, ", "))
	default:
		if covered := coveredRepos(scope.Known, sources); len(covered) >= 2 {
			system += fmt.Sprintf(answerCompare, strings.Join(covered, ", "))
		}
	}
	// What each repository in the project is for, and which calls which. Only
	// the turn's own projects reach it, so a pin that excludes a repository
	// never has it described anyway.
	system += scope.Structure
	if scope.Processes != "" {
		system += fmt.Sprintf(answerProcesses, scope.Processes)
	}
	if scope.Links != "" {
		system += fmt.Sprintf(answerLinks, scope.Links)
	}
	if scope.Located != "" {
		system += fmt.Sprintf(answerLocated, scope.Located)
	}
	if len(scope.Unknown) > 0 {
		system += fmt.Sprintf(answerMissingRepo, strings.Join(scope.Unknown, ", "))
	}
	if len(scope.Outside) > 0 {
		system += fmt.Sprintf(answerOutsideRepo, strings.Join(scope.Outside, ", "))
	}
	if scope.AllDenied && len(scope.Known) > 0 {
		system += fmt.Sprintf(answerAllDenied, strings.Join(scope.Known, ", "))
	}
	if followingUp != "" {
		system += fmt.Sprintf(answerFollowUp, followingUp)
	}
	system += rework
	// Computed from the sources rather than read off the scope, so this block
	// is right even for a caller that has not filled the field in.
	if DocsOnly(sources) {
		system += answerDocsOnly
	}
	// From the sources too: a stage label is a fact about a path and the
	// live declarations, and a re-explained turn carries the same.
	if staged(sources, scope.Stages) {
		if scope.Stage != "" {
			system += fmt.Sprintf(answerStageAsked, scope.Stage)
		} else {
			system += answerStages
		}
	}
	system += answerDiagram
	// The reader's own rules come after every rule of the product's, because
	// they outrank them, and before the closing language line, because that
	// is the one thing they do not.
	system += memories
	// Said twice, first and at the end: the sources in between are code and
	// comments in whatever language the repository uses, and a model that has
	// just read two thousand tokens of English tends to answer in it. How that
	// language is spelled follows it, closing the prompt.
	system += fmt.Sprintf(answerLanguage, name)
	return system
}

// stream makes the one Pro call and turns what comes back into the record.
// user is the whole user message; parts is the caller's measure of it.
func (a *Answerer) stream(ctx context.Context, lang Language, sources []Source,
	system, user string, parts PromptParts, onToken func(string)) (Answer, error) {

	// Every token passes the renumberer before it reaches the reader or the
	// record, so the two are the same text; what it holds back is flushed
	// once the stream ends, cut short or not. German is spelled the Swiss
	// way on the same pass (swiss.go), so the record is what was read.
	var text strings.Builder
	rn := newRenumberer(len(sources))
	sp := &speller{}
	if ParseLanguage(string(lang)) == LanguageDE {
		rn.spell = sp.prose
	}
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
		{Role: "user", Content: user},
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
		Prompt:    parts,
		Respelled: sp.rewrote,
	}, nil
}

// reachedVia says, for the answer prompt, how a source that the search did
// not return got there. A symbol hop names the symbol. A crossing names the
// queue or route AND the file on the near side, because that is the fact the
// answer has to state: shipping sends to the queue queue-master listens on,
// and no line of either file says so on its own.
func reachedVia(reason string) string {
	if rest, ok := strings.CutPrefix(reason, "edge:"); ok {
		return "reached in another repository, which shares the " + rest
	}
	// The gap pass: not a hop from anything, so there is no near side to
	// name. What the answer may say about it is that the code was fetched by
	// name after the rest had been read.
	if rest, ok := strings.CutPrefix(reason, "gap:"); ok {
		return "looked up by name after reading the sources: " + rest
	}
	// A census landing: the index recorded a navigation site here.
	if rest, ok := strings.CutPrefix(reason, "link:"); ok {
		return "link site " + rest
	}
	// The locate loop: looked for deliberately, after the first look had been
	// read, and the tool and its argument say what was asked for. The
	// argument is the index's spelling, never the model's prose.
	if rest, ok := strings.CutPrefix(reason, "locate:"); ok {
		return "looked for after reading the sources, by " + rest
	}
	return "reached via " + strings.TrimPrefix(reason, "reference:")
}

// staged reports whether any source lies under a declared stage directory.
func staged(sources []Source, declared stages.Set) bool {
	for _, s := range sources {
		if declared.Of(s.Repo, s.Path) != "" {
			return true
		}
	}
	return false
}

// renderSources numbers the gathered material. The number IS the citation
// marker the model writes, so it never has to invent an identifier for a
// file; the renumberer turns it into the reader's number on the way out.
func renderSources(question string, sources []Source, declared stages.Set) string {
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(question)
	b.WriteString("\n\nSources:\n")
	renderSourceList(&b, sources, declared)
	return b.String()
}

// renderRework is the user message of a rework: the previous turn whole,
// the instruction, and the sources that turn was written from, numbered the
// same way renderSources numbers them.
//
// The previous answer's citation markers are stripped. The record resolves
// its sources by hop and chunk id, not in the order the first turn numbered
// them, so a marker in the old text would point at the wrong entry of the
// list below; the model cites again from the list. Fences stay: a diagram or
// a code block is part of what "shorter" or "as a table" is asked of.
func renderRework(instruction string, t Thread, declared stages.Set) string {
	var b strings.Builder
	b.WriteString("Previous question: ")
	b.WriteString(t.Question)
	b.WriteString("\n\nPrevious answer:\n")
	b.WriteString(stripMarkersOutsideFences(t.Answer))
	b.WriteString("\n\nInstruction: ")
	b.WriteString(instruction)
	b.WriteString("\n\nSources:\n")
	renderSourceList(&b, t.Sources, declared)
	return b.String()
}

// stripMarkersOutsideFences removes the citation markers from an answer's
// prose and leaves its fenced blocks alone: a marker is [1], and so is an
// index expression in a code block a dev answer quoted.
func stripMarkersOutsideFences(answer string) string {
	var b strings.Builder
	last := 0
	for _, f := range fenceRe.FindAllStringIndex(answer, -1) {
		b.WriteString(markerGroupRe.ReplaceAllString(answer[last:f[0]], ""))
		b.WriteString(answer[f[0]:f[1]])
		last = f[1]
	}
	b.WriteString(markerGroupRe.ReplaceAllString(answer[last:], ""))
	return b.String()
}

// renderSourceList writes the numbered material, the number being the marker
// the model cites by.
func renderSourceList(b *strings.Builder, sources []Source, declared stages.Set) {
	for i, s := range sources {
		if s.IsCommit() {
			renderCommit(b, i+1, s)
			continue
		}
		fmt.Fprintf(b, "\n[%d] %s %s:%d-%d", i+1, s.Repo, s.Path, s.StartLine, s.EndLine)
		if s.Symbol != "" {
			fmt.Fprintf(b, " (%s)", s.Symbol)
		}
		// The same predicate fusion demotes by and the walk orders by: read
		// unlabelled, a test fake competes with the client it fakes for the
		// citation, and the model cannot tell the harness from the mechanism
		// by the excerpt alone.
		if retrieve.IsTestPath(s.Path) {
			b.WriteString(" (test)")
		}
		// The stage from the declared prefix, never from the model: a label
		// the answer rule keys on has to be a fact about the path.
		if st := declared.Of(s.Repo, s.Path); st != "" {
			fmt.Fprintf(b, " (stage %s)", st)
		}
		if s.Reason != "" && s.Reason != "hit" {
			fmt.Fprintf(b, " [%s]", reachedVia(s.Reason))
		}
		b.WriteString("\n")
		b.WriteString(s.Text)
		b.WriteString("\n")
	}
}
