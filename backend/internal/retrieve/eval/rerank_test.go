// The reranker arm: does one short-gate call over a deeper fused list move
// the unique misses into the cut, and does the answer then have them?
//
//	hack/run-eval.sh 'TestEvalMeasureRerank$'
package eval

import (
	"context"
	"fmt"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/retrieve"
)

func TestEvalMeasureRerank(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	client := llmClientForRouting(t)
	expansions := loadExpansions(t)
	codes := loadExpansionCodes(t)
	opts := gatherOpts(t)
	g := ask.NewGatherer(db, opts)

	type arm struct {
		name string
		r    *retrieve.Retriever
	}
	plain := evalRetriever(t, db)
	reranked := evalRetriever(t, db)
	// The pool is the reranker's; searchTexts lifts the lanes to it.
	rr := evalReranker(t, client)
	reranked.Reranker = rr
	// The keyword lane travels in the label the way the pool and the excerpt
	// do: a table run with a lane the product does not have must not read as
	// the product's.
	arms := []arm{
		{"fused order (baseline)" + codeLaneLabel(), plain},
		{fmt.Sprintf("fused order + short-gate rerank over %d, %d-rune excerpts%s", rr.Pool, rr.Excerpt, codeLaneLabel()), reranked},
	}

	// The same reading as every other arm in the package — measureArm — so a
	// reorder that lifts the unique misses cannot be read as a win while it
	// drops an ambiguous question's second alternative out of the cut.
	questions := loadQuestions(t)
	for _, a := range arms {
		t.Logf("\n=== %s ===", a.name)
		m := measureArm(t, ctx, a.name, g, questions, func(q Question) []retrieve.Hit {
			hits, err := a.r.Search(ctx, retrieve.Query{
				Texts:    expansionTextsOf(t, expansions, q),
				Code:     codes[q.Text],
				Question: q.Text,
				K:        gatherSearchK,
			})
			if err != nil {
				t.Fatalf("%s: search %q: %v", a.name, q.Text, err)
			}
			return hits
		})
		m.log(t, a.name)
	}
}
