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
	// Terms are the question restated in business language, which is what the
	// vector lane matches against doc comments and module names.
	Terms []string `json:"terms"`
	// CodeTerms are the identifiers the model GUESSES the code uses. This is
	// the bridge the phase-3 measurement showed to be missing: the question
	// says "Apple TV", the code says "AirPlay", and no embedding of the raw
	// question closes that gap on its own.
	CodeTerms []string `json:"code_terms"`
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
}

// CensusLink is the one census the pipeline has. Any other value in the
// reply is dropped on decode.
const CensusLink = "link"

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
		return nil
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
// The last entry is CodeText's, the same string Query.Code carries: the keyword
// lane finds the code rung by comparing the two, and building the text twice
// would make that equality a coincidence rather than a fact.
func (u Understanding) SearchTexts(question string) []string {
	texts := []string{question}
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
  intent      "how", "why", "where", "conformance", "changes" or "rework"
  since_days  for "changes" only: how many days back the question asks.
              "yesterday" is 1, "the last 2 days" is 2, "this week" is 7,
              "this month" is 30; nothing said is 0. Every other intent is 0.
  topic       for "changes" only: what the changes are about, as 1-4 words
              from the question ("snapshot handling", "login"), or "" when
              the question asks for every change. Every other intent is "".
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
              link works is not a census.

"changes" is a question about what was DONE to the code recently, not about
what the code does: "what changed", "what is new", "latest updates", "recent
commits", "was hat sich geändert", "was ist neu", "letzte Änderungen",
"quoi de neuf", "cosa è cambiato". A question about how a feature works is
never "changes", however recent the feature.

"rework" is a request to restate the PREVIOUS ANSWER in another form, asking
nothing new of the code: "summarize", "tl;dr", "shorter", "in one paragraph",
"as a table", "as bullet points", "simpler", "rephrase", "expand the second
point", "fasse zusammen", "kürzer", "résume", "riassumi".
It exists only when a previous turn is above; with none, or when the question
asks about anything the previous answer does not already say, it is not
"rework". A rework has terms [], code_terms [] and repos [].

A question may arrive with the previous turn of the conversation above it. That
material is there for ONE purpose: to resolve what the current question leaves
out - "that", "this", "it", "and how about the other one", a question with no
subject at all. Everything you answer with describes the CURRENT question. A
follow-up that stays on the subject inherits it; a follow-up that changes the
subject gets the new one, and the previous turn contributes nothing to it. A
follow-up that only moves the window of a changes question ("only the last
two days") keeps intent "changes" and the topic.

code_terms is the most important part. The question is phrased in the language
of the business domain, the code is not: someone asking about an "Apple TV"
means "AirPlay" in the code; someone asking about "disk almost full" means
"statfs" or "free bytes". Guess that bridge, even when you are not sure. Do not
simply repeat the words of the question.

No running text, no explanation, just the JSON object.`

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
	out, _, err := u.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: understandSystem + stageList(stageNames)},
		{Role: "user", Content: recall(question, t)},
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
	return got, nil
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
