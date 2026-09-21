// Package ask runs the question pipeline: understand, gather, answer.
//
// Only the last step streams. Everything before it is an ordinary request, so a
// failure there is a failure of the turn rather than of a half-written answer.
//
// The routing ladder's rungs are exported one by one — Ranked, Rank,
// Dominates, Related, Judge and Decide — and no production caller uses them:
// everything in the product goes through Route. They exist for the evaluation
// harness in internal/retrieve/eval, whose margin sweep has to run the
// expensive rungs once per question and then re-decide at six margins. Decide
// is the decision itself and is called by Route as well, so the harness
// cannot end up measuring a policy the product no longer runs. Do not delete
// them as unused, and do not let Route grow a decision that bypasses Decide.
package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/trick77/llmwire"

	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/memory"
)

// understandMaxTokens caps the JSON reply. It is a short structured object; a
// longer one means the model started explaining itself, which is not wanted
// here and not read by anyone.
const understandMaxTokens = 512

// gateTemperature pins every call in this package whose output is an id, a
// label or a one-word decision — the understanding, the routing judge, the
// candidate naming, the thread title. None of them is read as prose, and a
// re-roll on any of them is a defect rather than variety: phase 4c measured
// two runs of the routing arm over frozen expansions and an unchanged corpus
// deciding three of sixty-one questions differently, a spread wider than the
// difference phase 4b published between the two deployments. The reader meets
// the same thing as "ask twice, get a card once and an answer the other time".
//
// The answer call is NOT pinned. A person reads that one.
//
// The value is the intent, not the wire: llm.Policy (BACKEND_LLM_GATE_TEMPERATURE)
// decides what a pinned call actually sends, because 0 was measured on MiMo
// and another model may refuse it or want none.
const gateTemperature = 0

// Understanding is what the first step produces. Nobody reads it — it exists to
// aim the search.
type Understanding struct {
	// Intent is how, why, where, conformance, changes or rework. Phase 4a
	// answers how; changes is answered from the commit lane, never from the
	// files; rework is answered from the previous answer and its own sources,
	// with no search at all.
	Intent string `json:"intent"`
	// SinceDays is how far back a "what changed" question looks, in days.
	// Zero on every other intent; the pipeline applies the default window
	// when a changes question names none, so the model never has to.
	SinceDays Days `json:"since_days"`
	// Topic is what a changes question is about, or empty when it asks for
	// every change in the window. Filtered against commit subjects, bodies
	// and paths; "what changed" alone has no topic and lists the window.
	Topic string `json:"topic"`
	// Between is the two deployment stages a release question compares, as
	// declared names; empty on every other intent. A set, not a pair: which
	// stage is ahead is read from the tags' ancestry, never from the order
	// the model wrote them in. The reader's own stage words win over it.
	Between Names `json:"between"`
	// Terms are the question restated in business language, which is what the
	// vector lane matches against doc comments and module names.
	Terms []string `json:"terms"`
	// CodeTerms are the identifiers the model GUESSES the code uses. This is
	// the bridge the phase-3 measurement showed to be missing: the question
	// says "Apple TV", the code says "AirPlay", and no embedding of the raw
	// question closes that gap on its own.
	CodeTerms []string `json:"code_terms"`
	// Prior is the previous question of the thread, and it is NOT a model
	// output: Understand overwrites whatever the reply carried with the text
	// the record holds. A follow-up names its subject a turn ago - "in
	// welchem Formularschritt passiert das?" - and the expansion is the one
	// thing that cannot be trusted to keep it: the fields it fills are
	// specified as rewordings of the CURRENT question, so "dieser Vorgang"
	// satisfies them while naming nothing the index can match. Carried as its
	// own search lane instead, where a wrong guess cannot lose it.
	//
	// Stored in the clarification blob with everything else, so a resumed
	// turn searches what the thread was about; omitempty keeps rows written
	// before this existed decoding unchanged.
	Prior string `json:"prior,omitempty"`
	// Repos narrows the search when the question names a system. Empty means
	// the whole corpus.
	Repos []string `json:"repos"`
	// AllRepos is the reader asking for every repository at once, without
	// naming any of them: "in all repos", "across all products". Without it a
	// question that names nothing cannot be told apart from a question that
	// means everything, and the repository rung in Decide would card on both.
	// It is the reader's own permission to answer across the corpus, so it is
	// read from the question and never inferred from the hits.
	AllRepos bool `json:"all_repos"`
	// Stage is the deployment stage the question asks about, as one of the
	// names the prompt offered, or empty. Not a guess the way Repos is: the
	// prompt lists the declared names, and anything outside the list is
	// dropped by the pipeline. It covers what the reader's own words cannot
	// — "in the integration environment" for a stage called intg — because
	// the ordinary word is refused as an alias.
	Stage string `json:"stage"`
	// Census is "link" when the question asks to LIST the places a
	// repository navigates to — links to other applications, external
	// URLs — and empty otherwise. A listing is not a mechanism: search
	// returns twenty chunks, and a census of navigation sites is read from
	// the index instead (edges.InRepo). Read from the question, never from
	// the intent: "where is the total computed" is also "where".
	Census string `json:"census"`
	// Memory is a standing instruction the question carries — "never show
	// flowcharts", "don't mention lerb-chooser-ui anymore" — as one English
	// sentence, or "". Only with a memory holder on the context: a
	// deployment with memory off never asks for it, and a stray value is
	// dropped by the pipeline. MemoryScope is the project or repository the
	// instruction is limited to, as the reader named it; MemoryReplaces the
	// saved rules it contradicts; MemoryRemoves the saved rules the reader
	// asks to forget. The intent "memory" is a question that is ONLY a
	// directive, answered without a search.
	Memory         string `json:"memory"`
	MemoryScope    string `json:"memory_scope"`
	MemoryReplaces IDs    `json:"memory_replaces"`
	MemoryRemoves  IDs    `json:"memory_removes"`
}

// IDs is a list of row ids as a model writes one: numbers, numeric strings,
// or nothing. Anything else reads as nothing rather than failing the whole
// understanding, whose other fields the turn needs regardless.
type IDs []int64

// UnmarshalJSON reads an array of numbers or numeric strings, or null.
func (ids *IDs) UnmarshalJSON(b []byte) error {
	var raw []any
	if err := json.Unmarshal(b, &raw); err != nil {
		*ids = nil
		return nil //nolint:nilerr // malformed ids read as none: the rest of the understanding is what the turn needs (see the type comment)
	}
	out := make(IDs, 0, len(raw))
	for _, v := range raw {
		switch x := v.(type) {
		case float64:
			out = append(out, int64(x))
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
				out = append(out, int64(f))
			}
		}
	}
	*ids = out
	return nil
}

// Directive is what the understanding read as a standing instruction, or
// nothing.
func (u Understanding) Directive() memory.Directive {
	return memory.Directive{
		Text:     u.Memory,
		Scope:    u.MemoryScope,
		Replaces: []int64(u.MemoryReplaces),
		Removes:  []int64(u.MemoryRemoves),
	}
}

// CensusLink is the one census the pipeline has. Any other value in the
// reply is dropped on decode.
const CensusLink = "link"

// Names is a list of names a gate model may also write as one string
// ("prod, intg") or null: the release turn's one list field, and the one
// place a small model's formatting would otherwise fail the whole turn as
// "reply was not JSON". Anything unreadable is empty.
type Names []string

// UnmarshalJSON reads a list of strings, one comma-separated string, or null.
func (n *Names) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*n = list
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		*n = nil
		return nil //nolint:nilerr // a malformed name list reads as none: the rest of the understanding is what the turn needs (see the type comment)
	}
	*n = nil
	for _, part := range strings.Split(one, ",") {
		if p := strings.TrimSpace(part); p != "" {
			*n = append(*n, p)
		}
	}
	return nil
}

// Days is an integer a gate model may also write as a quoted string ("7") or
// a float (7.0): the only numeric field of the understanding, and the one
// place a small model's formatting would otherwise fail the whole turn as
// "reply was not JSON". Anything unreadable is 0, the default window.
type Days int

// UnmarshalJSON reads a number, a numeric string, or null.
func (d *Days) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*d = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*d = 0
		return nil //nolint:nilerr // a malformed day count reads as zero: the rest of the understanding is what the turn needs (see the type comment)
	}
	*d = Days(int(f))
	return nil
}

// SearchTexts assembles what the retriever should search for. The raw question
// comes first and is never dropped: the model's guesses are guesses, and a
// wrong one must not be able to replace what the user actually asked.
//
// Each text becomes its own semantic lane in retrieve.searchTexts, and the
// lanes are fused — so an expansion that adds nothing costs a lane, while one
// that lands pulls its file up through a lane of its own.
//
// The previous question follows it on a follow-up, because a follow-up
// references the turn before it by definition: "in welchem Formularschritt
// passiert das?" names its subject nowhere, and the words it does carry find
// whatever the corpus happens to match. The lane is not an embedding nudge —
// BuildFTSQueries runs over every text, so the subject gets a literal keyword
// match — and it is the one part of the search the expansion cannot lose. It
// survives a pin, where the raw question is dropped and the model's guesses
// would otherwise be the whole search, and it cannot widen one: knownRepos
// reads Query.Question alone, never the lanes.
//
// The last entry is CodeText's, the same string Query.Code carries: the keyword
// lane finds the code rung by comparing the two, and building the text twice
// would make that equality a coincidence rather than a fact.
func (u Understanding) SearchTexts(question string) []string {
	texts := []string{question}
	// Not when it IS this question, which is a reader asking the same thing
	// twice in one thread. A retry does NOT land here: it reads the last
	// answered turn strictly below the row it retries, never that row.
	if prior := strings.TrimSpace(u.Prior); prior != "" && prior != strings.TrimSpace(question) {
		texts = append(texts, prior)
	}
	if terms := strings.Join(u.Terms, " "); strings.TrimSpace(terms) != "" {
		texts = append(texts, terms)
	}
	if code := u.CodeText(); code != "" {
		texts = append(texts, code)
	}
	return texts
}

// CodeText is the guessed code vocabulary as one text, which is the entry
// SearchTexts puts last. It is handed to the retriever by name so the keyword
// lane can weigh a rung over guessed IDENTIFIERS differently from the same rung
// over the question's prose; empty when the step guessed none.
func (u Understanding) CodeText() string {
	code := strings.Join(u.CodeTerms, " ")
	if strings.TrimSpace(code) == "" {
		return ""
	}
	return code
}

// Understander runs the first step.
type Understander struct {
	llm *llm.Client
}

// NewUnderstander builds an Understander.
func NewUnderstander(c *llm.Client) *Understander {
	return &Understander{llm: c}
}

const understandSystem = `You analyse a question about a codebase and answer with JSON ONLY.

Fields:
  intent      "how", "why", "where", "conformance", "changes", "release"%s
  since_days  for "changes" only: how many days back the question asks.
              "yesterday" is 1, "the last 2 days" is 2, "this week" is 7,
              "this month" is 30; nothing said is 0. Every other intent is 0.
  topic       for "changes" only: what the changes are about, as 1-4 words
              from the question ("snapshot handling", "login"), or "" when
              the question asks for every change. Every other intent is "".
  between     for "release" only: the two deployment stages the question
              compares, each written exactly as one of the names listed
              below, else []. Every other intent is [].
  terms       2-4 rewordings of the question in domain language, as whole phrases
  code_terms  3-8 identifiers likely to occur in the source: class, method,
              package and protocol names, written the way a developer would
              write them
  repos       names of the repositories, if the question names any, else []
  all_repos   true when the question asks for every repository, or for several
              without naming them ("in all repos", "across all products",
              "in multiple repositories"), else false
  stage       the deployment stage the question asks about, written exactly as
              one of the names listed below, else "". A question that names
              no environment, or asks how something is integrated or tested
              rather than about the integration or test environment, has
              no stage.
  census      "link" when the question asks to LIST the places a repository
              links or navigates to — links to other applications, external
              URLs, outbound links — else "". A question about how one
              link works is not a census.%s

"changes" is a question about what was DONE to the code recently, not about
what the code does: "what changed", "what is new", "latest updates", "recent
commits", "was hat sich geändert", "was ist neu", "letzte Änderungen",
"quoi de neuf", "cosa è cambiato". A question about how a feature works is
never "changes", however recent the feature.

"release" is a question about what is deployed on one stage and not yet on
another, or for release notes between two stages or versions: "what is
between production and testing", "release notes for the next deployment",
"what goes live with the next release", "was ist zwischen prod und test",
"Release Notes", "notes de version", "note di rilascio". It names two
stages. A question about what changed in the code lately, with no two stages
in it, is "changes", never "release".

"rework" is a request to restate the PREVIOUS ANSWER in another form, asking
nothing new of the code: "summarize", "tl;dr", "shorter", "in one paragraph",
"as a table", "as bullet points", "simpler", "rephrase", "expand the second
point", "fasse zusammen", "kürzer", "résume", "riassumi".
It exists only when a previous turn is above; with none, or when the question
asks about anything the previous answer does not already say, it is not
"rework". A rework has terms [], code_terms [] and repos [].%s

A question may arrive with the previous turn of the conversation above it. That
material is there for ONE purpose: to resolve what the current question leaves
out - "that", "this", "it", "and how about the other one", a question with no
subject at all. Everything you answer with describes the CURRENT question. A
follow-up that stays on the subject inherits it; a follow-up that changes the
subject gets the new one, and the previous turn contributes nothing to it. A
follow-up that only moves the window of a changes question ("only the last
two days") keeps intent "changes" and the topic.%s

code_terms is the most important part. The question is phrased in the language
of the business domain, the code is not: someone asking about an "Apple TV"
means "AirPlay" in the code; someone asking about "disk almost full" means
"statfs" or "free bytes". Guess that bridge, even when you are not sure. Do not
simply repeat the words of the question.

No running text, no explanation, just the JSON object.`

// understandIntents closes the intent line, with or without "memory".
const (
	understandIntentsOff = ` or "rework"`
	understandIntentsOn  = `, "rework" or "memory"`
)

// understandFollowUp is ADDED to the follow-up paragraph above, and only on a
// turn that actually has a previous turn. The paragraph itself stays
// unconditional: removing it from a first turn would be an unmeasured change
// to the prompt the evaluation baseline was taken on, and it costs a first
// turn nothing to read a rule about material it does not have.
//
// It exists because "resolve what the current question leaves out" is a
// statement of purpose that no field contract enforces. terms is specified as
// rewordings of the CURRENT question, so a pronoun rewritten as "dieser
// Vorgang" satisfies it exactly while naming nothing the index can match.
// This says to CARRY the subject into the fields the search actually runs on.
//
// Second line of defence only: the previous question reaches the search as
// its own lane (Understanding.Prior) whatever this call returns.
//
// The last sentence is not decoration. Without it the rule reads as "carry
// the old subject" on a follow-up that CHANGED subject, which would put the
// old one into three of the four lanes instead of one: hits then span two
// repositories with none named in the current question, and the deterministic
// repository card fires where an answer belonged. The stamped lane costs one
// lane and cannot be talked out of; this rule can, so it says when not to
// apply.
const understandFollowUp = `
So when the current question points at something without naming it, name that
subject to yourself as the previous question named it, and then keep it: EVERY
entry of terms and EVERY entry of code_terms carries it. A rewording that
drops it is a rewording of a different question, and terms and code_terms are
what the search runs on - the words of the current question alone are not
enough to find what it points at. When the current question names its own
subject, this does not apply: expand THAT one and let the previous turn go.`

// understandMemory is the rule for the memory fields, in the system prompt
// only when the deployment keeps memory. Written in English whatever the
// question's language: one row serves threads in four languages.
const understandMemoryFields = `
  memory          a STANDING instruction the question gives about how to
                  answer from now on, as ONE English sentence in the
                  imperative, else "". It is standing when the reader says so:
                  "never", "always", "from now on", "don't ... anymore",
                  "stop ...ing", "nie wieder", "ab jetzt", "künftig",
                  "désormais", "ne ... plus jamais", "d'ora in poi", "mai
                  più". Examples: "Zeig mir nie wieder Flowcharts" is "Never
                  draw flowchart diagrams."; "the library lerb-chooser-ui
                  doesn't interest me, don't mention it anymore" is "Do not
                  mention the library lerb-chooser-ui.". A request for THIS
                  answer only ("no diagram this time", "shorter", "as a
                  table") is not a memory. A question is never a memory. An
                  instruction about the answer language, the audience or
                  which repositories to search is not a memory: those are
                  settings the reader picks. Never a statement about what the
                  code does, never a credential.
  memory_scope    the project or repository the instruction is limited to,
                  written exactly as the reader named it, else "".
  memory_replaces ids of saved instructions (listed below the question when
                  there are any) that the new instruction contradicts or
                  restates, else [].
  memory_removes  ids of saved instructions the reader asks to forget
                  ("forget the rule about diagrams", "show flowcharts
                  again"), else [].`

// understandMemoryIntent defines the intent, after the rework's.
const understandMemoryIntent = `

"memory" is the intent of a question that is ONLY an instruction — it asks
nothing of the code. It has terms [], code_terms [] and repos []. A question
that carries an instruction AND asks something keeps the intent of what it
asks, with the instruction in memory.`

// answerRecall is how much of the previous answer the understanding step is
// shown. A BA answer is the core mechanism in three to five paragraphs, so the
// opening carries the subject; the rest is edge cases, and this call is a gate
// that emits four JSON fields, not a reader.
const answerRecall = 1200

// fenceRe matches a fenced block in an answer, closed or running to the end of
// the text. Unclosed counts: the excerpt is cut to length anyway, so an answer
// whose fence is the last thing in it would otherwise contribute its opening
// brace and nothing else.
var fenceRe = regexp.MustCompile("(?s)```.*?(```|$)")

// recall is the user message: the previous turn, when there is one, above the
// question being asked now.
//
// One message, labelled, rather than a user/assistant pair of real turns. The
// difference matters here: this call runs at temperature 0 with thinking off
// and its reply has to parse as JSON, and an assistant turn of prose directly
// before the question invites the model to continue the conversation instead of
// answering with a schema. Labelled context inside one user message is read as
// material, which is what it is.
//
// The previous answer arrives stripped and cut short. Citation markers number
// sources this call cannot see and will not use. Fenced blocks are worse than
// useless: an answer's diagram fence is a JSON spec, and 1200 characters of
// {"nodes":[{"id":...}]} is what the excerpt would otherwise consist of for
// any follow-up to a turn that drew one. What is left is the prose, which is
// the only part that says what the turn was about.
func recall(question string, t Thread) string {
	if t.Question == "" {
		return question
	}
	var b strings.Builder
	b.WriteString("Previous question: ")
	b.WriteString(t.Question)
	b.WriteString("\n")
	prose := markerGroupRe.ReplaceAllString(fenceRe.ReplaceAllString(t.Answer, " "), "")
	if prev := excerptOf(prose, answerRecall); prev != "" {
		b.WriteString("Previous answer (excerpt): ")
		b.WriteString(prev)
		b.WriteString("\n")
	}
	b.WriteString("\nQuestion: ")
	b.WriteString(question)
	return b.String()
}

// Understand turns a question into search material.
//
// Runs on the short-gate deployment: the output is a structured blob nobody
// reads, so the expensive queue would buy nothing. Thinking is disabled as a
// SEPARATE decision, for a reason of its own — MiMo's reasoning channel can
// bleed into the content, and here the content has to parse as JSON.
func (u *Understander) Understand(ctx context.Context, question string, t Thread, stageNames []string) (Understanding, error) {
	holder := memory.From(ctx)
	out, _, err := u.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: understandPrompt(holder != nil, t.Question != "") + stageList(stageNames)},
		{Role: "user", Content: recall(question, t) + memoryList(holder)},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(understandMaxTokens), llm.WithStep("understand"))
	if err != nil {
		return Understanding{}, fmt.Errorf("understand the question: %w", err)
	}

	var got Understanding
	body, _ := llmwire.JSONObject(out)
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		// Not a silent fallback to the raw question: that is precisely the
		// behaviour this step replaces, and it would show up later as "the
		// expansion did not help" rather than as "the expansion never ran".
		return Understanding{}, fmt.Errorf("understand the question: reply was not JSON: %w", err)
	}
	// Normalised once, here, because everything downstream compares it to a
	// word: the answer prompt looks the intent up in a table, and `Where.`,
	// `"where"` and `where` are one reading. The trimmed set is what a model
	// asked for one word out of four actually returns — spacing, the quotes
	// of a half-escaped string, and the full stop of a sentence that was
	// never wanted. A word the table does not carry is left as it is: it
	// reaches the trace, where a reader sees what the model said.
	got.Intent = strings.Trim(strings.ToLower(got.Intent), " \t\n\"'`.,:;!?")
	// Same trim for the census, and only the one kind that has a census
	// behind it survives: a model volunteering "route" would otherwise send
	// the pipeline to a listing that does not exist.
	if got.Census = strings.Trim(strings.ToLower(got.Census), " \t\n\"'`.,:;!?"); got.Census != CensusLink {
		got.Census = ""
	}
	// Stamped, never read: overwriting whatever the reply carried is the
	// point. The thread's own question is a fact the record holds, and the
	// one part of a follow-up's search that must not depend on the model
	// having kept the subject. Empty for a first turn, so its lanes are
	// exactly what they were.
	got.Prior = strings.TrimSpace(t.Question)
	return got, nil
}

// understandPrompt is the system prompt with or without the memory fields.
// Without them the prompt is byte for byte what it was before memory
// existed, which is what keeps the eval baseline comparable.
func understandPrompt(withMemory, withPrior bool) string {
	followUp := ""
	if withPrior {
		followUp = understandFollowUp
	}
	if !withMemory {
		return fmt.Sprintf(understandSystem, understandIntentsOff, "", "", followUp)
	}
	return fmt.Sprintf(understandSystem, understandIntentsOn, understandMemoryFields, understandMemoryIntent, followUp)
}

// memoryList closes the user message with the saved rules, ids first, so
// the model can say which one a new instruction contradicts. Nothing when
// memory is off or empty.
func memoryList(h *memory.Holder) string {
	rows := h.Rows()
	if len(rows) == 0 {
		return ""
	}
	return "\n\n" + memory.ListLine(rows)
}

// stageList closes the system prompt with the declared stage names, or with
// the statement that there are none — so the model has a list to copy from
// and never a word to invent.
func stageList(names []string) string {
	if len(names) == 0 {
		return "\n\nDeclared stages: none. stage is always \"\"."
	}
	return "\n\nDeclared stages: " + strings.Join(names, ", ") + "."
}
