// The reranker arm: does one short-gate call over a deeper fused list move
// the unique misses into the cut, and does the answer then have them?
//
//	hack/run-eval.sh 'TestEvalMeasureRerank$'
package eval

import (
	"context"
	"os"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/retrieve"
)

func TestEvalMeasureRerank(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	client := llmClientForRouting(t)
	embedder := embed.NewClient(embed.Config{
		BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
		APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
		Model:   envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"),
		Dim:     dim,
	}, nil)
	expansions := loadExpansions(t)
	opts := gatherOpts(t)
	g := ask.NewGatherer(db, opts)

	type arm struct {
		name string
		r    *retrieve.Retriever
	}
	plain := retrieve.New(db, embedder)
	reranked := retrieve.New(db, embedder)
	reranked.Candidates = 60
	reranked.Reranker = evalReranker(t, client)
	arms := []arm{{"fused order (the product)", plain}, {"fused order + short-gate rerank over 60", reranked}}

	questions := loadQuestions(t)
	for _, a := range arms {
		var n, at5, at20, gathered int
		var mrr float64
		// The other cohorts, so a reorder that lifts the unique misses is not
		// read as a win while it drops an ambiguous question's second
		// alternative or a composition's far half out of the cut.
		var ambN, ambBoth, compN, compAll int
		t.Logf("\n=== %s ===", a.name)
		for _, q := range questions {
			texts, ok := expansions[q.Text]
			if !ok {
				t.Fatalf("no expansion for %q", q.Text)
			}
			hits, err := a.r.Search(ctx, retrieve.Query{Texts: texts, Question: q.Text, K: gatherSearchK})
			if err != nil {
				t.Fatalf("%s: search %q: %v", a.name, q.Text, err)
			}
			if q.Resolution != ResolutionUnique {
				sources, err := g.Gather(ctx, hits)
				if err != nil {
					t.Fatalf("%s: gather %q: %v", a.name, q.Text, err)
				}
				found := 0
				for _, c := range q.Candidates {
					if hopOfCandidate(sources, c) >= 0 {
						found++
					}
				}
				switch q.Resolution {
				case ResolutionAmbiguous:
					ambN++
					if found >= 2 {
						ambBoth++
					}
				case ResolutionComposition:
					compN++
					if found == len(q.Candidates) {
						compAll++
					}
				}
				continue
			}
			n++
			rank := 0
			for i, h := range hits {
				if h.Repo == q.Candidates[0].Repo && contains(q.Candidates[0].Paths, h.Path) {
					rank = i + 1
					break
				}
			}
			if rank > 0 && rank <= 5 {
				at5++
			}
			if rank > 0 {
				at20++
				mrr += 1 / float64(rank)
			}
			sources, err := g.Gather(ctx, hits)
			if err != nil {
				t.Fatalf("%s: gather %q: %v", a.name, q.Text, err)
			}
			if hopOfCandidate(sources, q.Candidates[0]) >= 0 {
				gathered++
			}
			t.Logf("  rank %3d gathered %-5v %s", rank, hopOfCandidate(sources, q.Candidates[0]) >= 0, short(q.Text))
		}
		t.Logf("  %s: unique n=%d recall@5 %.3f (%d) recall@20 %.3f (%d) MRR %.3f gathered %.3f (%d)",
			a.name, n, float64(at5)/float64(n), at5, float64(at20)/float64(n), at20, mrr/float64(n), float64(gathered)/float64(n), gathered)
		t.Logf("  %s: ambiguous both alternatives gathered %d/%d; composition all parts gathered %d/%d",
			a.name, ambBoth, ambN, compAll, compN)
	}
}
