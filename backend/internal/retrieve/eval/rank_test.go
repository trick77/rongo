// Where the unique misses sit: the diagnostic that decides whether a reranker
// is worth building.
//
// A reranker can only reorder what the lanes returned. If the expected file
// of a missed question is in the candidate pool at rank 25, reordering can
// rescue it; if it is not in the pool at all, no reranker will, and the arm is
// not built. This asks the retriever for a deep cut under the product's
// expansion and reports, per question the product misses, where the expected
// file sits — the same question the August document asked of the raw query.
//
//	hack/run-eval.sh 'TestEvalRankOfMisses$'
package eval

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// deepK is how far down the fused list this looks. Past it a file is out of
// reach of any reordering a turn could afford.
const deepK = 200

func TestEvalRankOfMisses(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	r := retrieve.New(db, evalEmbedder(t, envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"), dim))
	// The pool has to be as deep as the cut, or the cut is the pool.
	r.Candidates = deepK
	expansions := loadExpansions(t)

	var inPool, beyond, missing int
	for _, q := range loadQuestions(t) {
		if q.Resolution != ResolutionUnique {
			continue
		}
		texts, ok := expansions[q.Text]
		if !ok {
			t.Fatalf("no expansion for %q", q.Text)
		}
		hits, err := r.Search(ctx, retrieve.Query{Texts: texts, Question: q.Text, K: deepK})
		if err != nil {
			t.Fatalf("search %q: %v", q.Text, err)
		}
		rank := 0
		for i, h := range hits {
			if h.Repo == q.Candidates[0].Repo && contains(q.Candidates[0].Paths, h.Path) {
				rank = i + 1
				break
			}
		}
		switch {
		case rank == 0:
			missing++
			t.Logf("  not in the top %d   %s", deepK, short(q.Text))
		case rank > gatherSearchK:
			beyond++
			t.Logf("  rank %3d             %s", rank, short(q.Text))
		default:
			inPool++
		}
	}
	t.Logf("unique questions: %d inside the top %d, %d between %d and %d, %d absent from the top %d",
		inPool, gatherSearchK, beyond, gatherSearchK+1, deepK, missing, deepK)
}

func contains(paths []string, p string) bool {
	for _, x := range paths {
		if x == p {
			return true
		}
	}
	return false
}
