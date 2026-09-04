package eval

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/retrieve"
)

// repoDecays is the sweep. 1.0 is the shipped behaviour and is measured in the
// same run rather than quoted from the previous document: the corpus, the
// expansions and the embedding model all moved since those numbers were
// written, and an arm compared against a remembered baseline measures the
// memory.
var repoDecays = []float64{1.0, 0.9, 0.8, 0.7, 0.6, 0.5, 0.3}

// diversityK is the cut the metric is read at. Twenty, because that is where
// the corpus-swap measurement found the constraint: of sixteen ambiguous
// questions only seven retrieved BOTH of their alternatives into the top 20,
// so the router was asked to arbitrate between candidates that were not in
// the list. That seven is the unexpanded question; with the shipped expansion
// the same corpus measures twelve, which is why the 1.0 arm below is measured
// rather than quoted.
const diversityK = 20

// TestEvalMeasureDiversitySweep reports, per decay, how many ambiguous
// questions retrieve every one of their alternatives — the number the change
// exists to move — alongside the two numbers that must not fall while it
// moves: recall@20 on the unique cohort, and the same all-parts-found metric
// on the composition cohort, where losing a part is worse than on an ambiguous
// question because no clarification will recover it.
//
// Each arm re-embeds the question texts. That is a handful of cents and a few
// minutes; sharing one embedding across arms would mean rebuilding the lanes
// by hand here and measuring a fusion the product does not run.
func TestEvalMeasureDiversitySweep(t *testing.T) {
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

	t.Logf("questions=%d k=%d decays=%v", len(questions), diversityK, repoDecays)
	t.Logf("")
	t.Logf("%-7s %-14s %-14s %-14s %s", "decay", "ambig all", "ambig parts", "unique r@20", "composition all")

	for _, decay := range repoDecays {
		r := retrieve.New(db, embedder)
		r.RepoDecay = decay

		var ambigAll, ambigN, ambigParts, ambigPartsN int
		var uniqueHit, uniqueN int
		var compAll, compN int

		for _, q := range questions {
			hits := diverseHits(t, ctx, r, expansions, expansionRepos, q)
			found := candidatesFound(hits, q)

			switch q.Resolution {
			case ResolutionUnique:
				uniqueN++
				if rankOfExpected(hits, q) > 0 {
					uniqueHit++
				}
			case ResolutionAmbiguous:
				ambigN++
				ambigPartsN += len(q.Candidates)
				ambigParts += found
				if found == len(q.Candidates) {
					ambigAll++
				}
			case ResolutionComposition:
				compN++
				if found == len(q.Candidates) {
					compAll++
				}
			}
		}

		t.Logf("%-7.2f %-14s %-14s %-14s %s",
			decay,
			frac(ambigAll, ambigN),
			frac(ambigParts, ambigPartsN),
			frac(uniqueHit, uniqueN),
			frac(compAll, compN))
	}
}

// diverseHits searches with the retriever's own decay. It is hitsFor with the
// diversity cut rather than the routing one, kept separate so a change to the
// routing arm's depth cannot silently move this measurement.
func diverseHits(t *testing.T, ctx context.Context, r *retrieve.Retriever, expansions, repos map[string][]string, q Question) []retrieve.Hit {
	t.Helper()
	texts, ok := expansions[q.Text]
	if !ok {
		t.Fatalf("no expansion recorded for %q — run TestExpandQuestions first", q.Text)
	}
	hits, err := r.Search(ctx, retrieve.Query{Texts: texts, Repos: repos[q.Text], Question: q.Text, K: diversityK})
	if err != nil {
		t.Fatalf("search %q: %v", q.Text, err)
	}
	return hits
}

// frac renders a count as "0.438 (7/16)". A bare ratio hides how few questions
// a cohort has, and seven of sixteen is a different claim from 438 of 1000.
func frac(n, total int) string {
	if total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.3f (%d/%d)", float64(n)/float64(total), n, total)
}
