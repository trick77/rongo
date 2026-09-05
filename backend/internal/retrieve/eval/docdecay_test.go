package eval

import (
	"context"
	"os"
	"testing"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/retrieve"
)

// docDecays is the sweep. 1.0 is what ships and is measured in the same run
// rather than quoted: an arm compared against a remembered baseline measures
// the memory, the same reason repoDecays keeps its own 1.0 arm.
var docDecays = []float64{1.0, 0.7, 0.5, 0.35, 0.2}

// docK is the cut the metric is read at — the routing depth, because that is
// where the demotion has to pay off. A document that keeps its place at 20 and
// loses it at 5 has still filled the material an answer is written from.
const docK = 20

// TestEvalMeasureDocSweep reports, per documentation decay, the two numbers
// that decide the constant, which pull in opposite directions:
//
//   - code-led recall: on the questions whose answer is in code, does the code
//     reach the cut? This is what the demotion exists to raise. Prose in domain
//     vocabulary is what a natural-language question matches, so documentation
//     fills the cut the code should be in — and it compounds, because a
//     document carries no ctags symbol and the reference walk in internal/ask
//     joins the symbols table, so nothing hops out of one.
//   - doc-led recall: on the questions whose answer exists ONLY in a document,
//     is the document still found? This is the axis that stops the sweep from
//     naming a filter. AGENTS.md forbids one outright: "never a broad doc
//     exclusion", and "what does the README say about X" is a real question.
//
// The value to ship is the harshest decay that leaves doc-led recall intact.
// A decay that raises code-led recall by taking doc-led recall with it has not
// improved retrieval, it has changed which questions rongo can answer.
//
// Each arm re-embeds the question texts, for the reason the diversity sweep
// gives: sharing one embedding across arms would mean rebuilding the lanes by
// hand and measuring a fusion the product does not run.
func TestEvalMeasureDocSweep(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()

	embedder := embed.NewClient(embed.Config{
		BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
		APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
		Model:   envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"),
		Dim:     dim,
	}, nil)
	expansions := loadExpansions(t)
	expansionRepos := loadExpansionRepos(t)
	questions := loadQuestions(t)

	var docLed, codeLed []Question
	for _, q := range questions {
		if docLedQuestion(q) {
			docLed = append(docLed, q)
		} else {
			codeLed = append(codeLed, q)
		}
	}
	if len(docLed) == 0 {
		t.Fatal("no doc-led question in the cohort: the sweep would have no downside axis and would name a filter")
	}

	t.Logf("code-led=%d doc-led=%d k=%d decays=%v", len(codeLed), len(docLed), docK, docDecays)
	t.Logf("")
	t.Logf("%-7s %-20s %s", "decay", "code-led r@20", "doc-led r@20")

	for _, decay := range docDecays {
		r := retrieve.New(db, embedder)
		r.DocDecay = decay

		var codeHit, docHit int
		for _, q := range codeLed {
			if rankOfExpected(docHits(t, ctx, r, expansions, expansionRepos, q), q) > 0 {
				codeHit++
			}
		}
		for _, q := range docLed {
			if rankOfExpected(docHits(t, ctx, r, expansions, expansionRepos, q), q) > 0 {
				docHit++
			}
		}

		t.Logf("%-7.2f %-20s %s", decay, frac(codeHit, len(codeLed)), frac(docHit, len(docLed)))
	}
}

// docLedQuestion reports whether every path a question expects is a document,
// which makes it one the demotion could silently take away. Read off the
// expected paths rather than off a flag in the JSON: the cohort's shape is
// what it says its answers are, and a flag would be one more thing to keep
// truthful by hand.
func docLedQuestion(q Question) bool {
	n := 0
	for _, c := range q.Candidates {
		for _, p := range c.Paths {
			n++
			if !retrieve.IsDocPath(p) {
				return false
			}
		}
	}
	return n > 0
}

// docHits searches with the retriever's own doc decay, at the routing cut.
// Kept separate from diverseHits so a change to either measurement's depth
// cannot move the other one.
func docHits(t *testing.T, ctx context.Context, r *retrieve.Retriever, expansions, repos map[string][]string, q Question) []retrieve.Hit {
	t.Helper()
	texts, ok := expansions[q.Text]
	if !ok {
		t.Fatalf("no expansion recorded for %q — run TestExpandQuestions first", q.Text)
	}
	hits, err := r.Search(ctx, retrieve.Query{Texts: texts, Repos: repos[q.Text], Question: q.Text, K: docK})
	if err != nil {
		t.Fatalf("search %q: %v", q.Text, err)
	}
	return hits
}

// TestCohortHasADocLedAxis runs without an embedding endpoint, so the one
// property the sweep cannot check for itself is checked on every ordinary
// test run: that the cohort still contains questions only a document answers.
// Without them TestEvalMeasureDocSweep would report that the harshest decay
// wins, which is how a demotion becomes the exclusion AGENTS.md forbids.
func TestCohortHasADocLedAxis(t *testing.T) {
	n := 0
	for _, q := range loadQuestions(t) {
		if docLedQuestion(q) {
			n++
		}
	}
	if n < 3 {
		t.Errorf("%d doc-led questions in the cohort, want at least 3", n)
	}
}
