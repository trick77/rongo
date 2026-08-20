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
	"strings"

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
const gateTemperature = 0

// Understanding is what the first step produces. Nobody reads it — it exists to
// aim the search.
type Understanding struct {
	// Intent is how, why, where or conformance. Phase 4a answers how.
	Intent string `json:"intent"`
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
}

// SearchTexts assembles what the retriever should search for. The raw question
// comes first and is never dropped: the model's guesses are guesses, and a
// wrong one must not be able to replace what the user actually asked.
//
// Each text becomes its own semantic lane in retrieve.searchTexts, and the
// lanes are fused — so an expansion that adds nothing costs a lane, while one
// that lands pulls its file up through a lane of its own.
func (u Understanding) SearchTexts(question string) []string {
	texts := []string{question}
	if terms := strings.Join(u.Terms, " "); strings.TrimSpace(terms) != "" {
		texts = append(texts, terms)
	}
	if code := strings.Join(u.CodeTerms, " "); strings.TrimSpace(code) != "" {
		texts = append(texts, code)
	}
	return texts
}

// Understander runs the first step.
type Understander struct {
	llm *llm.Client
}

// NewUnderstander builds an Understander.
func NewUnderstander(c *llm.Client) *Understander {
	return &Understander{llm: c}
}

const understandSystem = `Du analysierst eine Frage zu einer Codebasis und antwortest AUSSCHLIESSLICH mit JSON.

Felder:
  intent      "how", "why", "where" oder "conformance"
  terms       2-4 Umformulierungen der Frage in Fachsprache, als ganze Wortgruppen
  code_terms  3-8 Bezeichner, die im Quelltext wahrscheinlich vorkommen: Klassen-,
              Methoden-, Paket- und Protokollnamen, englisch, so wie ein Entwickler
              sie schreiben wuerde
  repos       Namen der Repositories, falls die Frage eines nennt, sonst []

code_terms ist der wichtigste Teil. Die Frage ist in der Sprache des Fachbereichs
gestellt, der Code ist es nicht: Wer nach einem "Apple TV" fragt, meint im Code
"AirPlay"; wer nach "Platte fast voll" fragt, meint "statfs" oder "free bytes".
Rate diese Bruecke, auch wenn du dir nicht sicher bist. Wiederhole nicht einfach
die Woerter der Frage.

Kein Fliesstext, keine Erklaerung, nur das JSON-Objekt.`

// Understand turns a question into search material.
//
// Runs on the short-gate deployment: the output is a structured blob nobody
// reads, so the expensive queue would buy nothing. Thinking is disabled as a
// SEPARATE decision, for a reason of its own — MiMo's reasoning channel can
// bleed into the content, and here the content has to parse as JSON.
func (u *Understander) Understand(ctx context.Context, question string) (Understanding, error) {
	out, _, err := u.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: understandSystem},
		{Role: "user", Content: question},
	}, llm.ShortGate(), llm.WithoutThinking(), llm.WithTemperature(gateTemperature), llm.WithMaxTokens(understandMaxTokens))
	if err != nil {
		return Understanding{}, fmt.Errorf("understand the question: %w", err)
	}

	var got Understanding
	if err := json.Unmarshal([]byte(stripFence(out)), &got); err != nil {
		// Not a silent fallback to the raw question: that is precisely the
		// behaviour this step replaces, and it would show up later as "the
		// expansion did not help" rather than as "the expansion never ran".
		return Understanding{}, fmt.Errorf("understand the question: reply was not JSON: %w", err)
	}
	return got, nil
}

// stripFence unwraps a ```json fenced block. Models emit them often enough that
// treating one as a parse failure would make this step flaky for no reason.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}
