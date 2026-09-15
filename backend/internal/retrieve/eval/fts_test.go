// The code-rung arm: the keyword lane's recall floor over the GUESSED
// IDENTIFIERS is a stronger claim than the same floor over the question's
// prose — "one of these words appears here" says more when the words are
// PromoMailJob and dispatchRetry than when they are "mail" and "sent". Does
// weighing it accordingly find code the fused list was missing, and does it
// cost anything where the lane already worked?
//
//	hack/run-eval.sh 'TestEvalMeasureFTS$'
//
// Deterministic by construction: no reranker, no model call, one database. The
// arms differ in one lane weight, nothing else.
package eval

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/retrieve"
)

// ftsArm is one configuration of the keyword lane.
type ftsArm struct {
	name string
	r    *retrieve.Retriever
}

// TestEvalMeasureFTS reports, per arm, recall@5, recall@20, MRR and how much
// of the material an answer is written from the gatherer actually reaches —
// plus the two cohorts that stop a recall win from hiding a loss, and the
// doc-led axis, because a rung that weighs identifiers up weighs prose down
// relative to it, and a question only a document answers has none.
//
// Ranks are kept per arm and the mean is taken over the questions EVERY arm
// ranks, for the reason the documentation sweep gives: averaging each arm's own
// found set compares two different question sets, and an arm that admits one
// more question at rank 19 then reports a worse mean while nothing it shared
// with the previous arm moved at all.
func TestEvalMeasureFTS(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()

	expansions := loadExpansions(t)
	expansionCodes := loadExpansionCodes(t)
	expansionRepos := loadExpansionRepos(t)
	questions := loadQuestions(t)
	g := ask.NewGatherer(db, gatherOpts(t))

	// Both arms in the same run, the reason repoDecays keeps its own 1.0 arm:
	// an arm compared against a remembered baseline measures the memory.
	plain := retrieve.New(db, evalEmbedder(t))
	rung := retrieve.New(db, evalEmbedder(t))
	rung.CodeWeight = retrieve.WeightKeywordCode
	arms := []ftsArm{
		{"today's lane", plain},
		{fmt.Sprintf("code rung %.1f", retrieve.WeightKeywordCode), rung},
	}

	ranks := make([]map[string]int, len(arms))
	for i, a := range arms {
		ranks[i] = map[string]int{}
		var n, at5, at20, gathered int
		var mrr float64
		var ambN, ambBoth, compN, compAll int
		var docN, docAt20 int
		t.Logf("\n=== %s ===", a.name)

		for _, q := range questions {
			texts, ok := expansions[q.Text]
			if !ok {
				t.Fatalf("no expansion for %q", q.Text)
			}
			hits, err := a.r.Search(ctx, retrieve.Query{
				Texts:    texts,
				Code:     expansionCodes[q.Text],
				Repos:    expansionRepos[q.Text],
				Question: q.Text,
				K:        gatherSearchK,
			})
			if err != nil {
				t.Fatalf("%s: search %q: %v", a.name, q.Text, err)
			}

			// The doc-led axis, read off the same hit lists: aux puts a path
			// into the lane, and a document is mostly path. A win on code that
			// takes the documents with it has changed which questions rongo can
			// answer, not improved retrieval.
			if docLedQuestion(q) {
				docN++
				if rankOfExpected(hits, q) > 0 {
					docAt20++
				}
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
				// Per question here too, not just in the unique cohort: a gain
				// of one ambiguous pair is a claim about ONE question, and
				// without the line nothing in the output says which.
				t.Logf("  %-9s %d/%d gathered %s", q.Resolution, found, len(q.Candidates), short(q.Text))
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
				ranks[i][q.Text] = rank
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
			a.name, n, float64(at5)/float64(n), at5, float64(at20)/float64(n), at20, mrr/float64(n),
			float64(gathered)/float64(n), gathered)
		t.Logf("  %s: ambiguous both alternatives gathered %d/%d; composition all parts gathered %d/%d",
			a.name, ambBoth, ambN, compAll, compN)
		t.Logf("  %s: doc-led recall@%d %s", a.name, gatherSearchK, frac(docAt20, docN))
	}

	// The common set: the questions every arm ranks, so the means below are the
	// same questions moving rather than different ones being counted.
	var common []string
	for text := range ranks[0] {
		in := true
		for _, m := range ranks[1:] {
			if _, ok := m[text]; !ok {
				in = false
				break
			}
		}
		if in {
			common = append(common, text)
		}
	}
	sort.Strings(common)

	t.Logf("")
	t.Logf("mean rank of the expected code over the %d questions every arm ranks", len(common))
	for i, a := range arms {
		sum := 0
		for _, text := range common {
			sum += ranks[i][text]
		}
		mean := 0.0
		if len(common) > 0 {
			mean = float64(sum) / float64(len(common))
		}
		t.Logf("  %-28s %.2f", a.name, mean)
	}
}
