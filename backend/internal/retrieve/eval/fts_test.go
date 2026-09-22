// The code-rung arm: the keyword lane's recall floor over the GUESSED
// IDENTIFIERS is a stronger claim than the same floor over the question's
// prose — "one of these words appears here" says more when the words are
// PromoMailJob and dispatchRetry than when they are "mail" and "sent". Does
// weighing it accordingly find code the fused list was missing, and does it
// cost anything where the lane already worked?
//
//	scripts/run-eval.sh 'TestEvalMeasureFTS$'
//
// Deterministic by construction: no reranker, no model call, one database. The
// arms differ in one lane weight, nothing else.
package eval

import (
	"context"
	"fmt"
	"os"
	"strconv"
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
	// an arm compared against a remembered baseline measures the memory. The
	// rung ships, so the baseline is the one that has to be configured: zero is
	// the prose floor the lane had before it.
	plain := retrieve.New(db, evalQueryEmbedder(t, db))
	plain.CodeWeight = 0
	plain.SubstringWeight = 0
	// The substring rung gets its own pair, for the reason the code rung has
	// one: it reaches chunks no FTS rung can see at all, so "with and without"
	// is the only way to price it. Both arms otherwise the product's.
	noSub := retrieve.New(db, evalQueryEmbedder(t, db))
	noSub.SubstringWeight = 0
	// The swept arm reads BACKEND_EVAL_SUBSTRING_WEIGHT so the constant can be
	// settled without a rebuild; unset, it is the product's.
	swept := retrieve.New(db, evalQueryEmbedder(t, db))
	subW := swept.SubstringWeight
	if w := os.Getenv("BACKEND_EVAL_SUBSTRING_WEIGHT"); w != "" {
		f, err := strconv.ParseFloat(w, 64)
		if err != nil {
			t.Fatalf("BACKEND_EVAL_SUBSTRING_WEIGHT=%q: %v", w, err)
		}
		subW = f
		swept.SubstringWeight = f
	}
	arms := []ftsArm{
		{"prose floor only (the lane before the rung)", plain},
		{fmt.Sprintf("code rung %.1f, no substring rung", retrieve.WeightKeywordCode), noSub},
		{fmt.Sprintf("code rung %.1f + substring rung %.2f", retrieve.WeightKeywordCode, subW), swept},
	}
	// Both arms always run: this test IS the comparison, so the switch every
	// other arm in the package reads would only let one half of it disappear.
	t.Logf("both arms, the switch does not apply here: BACKEND_EVAL_CODE_LANE is for the arms that measure something else")

	ranks := make([]map[string]int, len(arms))
	for i, a := range arms {
		t.Logf("\n=== %s ===", a.name)
		m := measureArm(ctx, t, a.name, g, questions, func(q Question) []retrieve.Hit {
			hits, err := a.r.Search(ctx, retrieve.Query{
				Texts:    expansionTextsOf(t, expansions, q),
				Code:     expansionCodes[q.Text],
				Repos:    expansionRepos[q.Text],
				Question: q.Text,
				K:        gatherSearchK,
			})
			if err != nil {
				t.Fatalf("%s: search %q: %v", a.name, q.Text, err)
			}
			return hits
		})
		m.log(t, a.name)
		ranks[i] = m.ranks
	}

	common := commonRanked(ranks)
	t.Logf("")
	t.Logf("mean rank of the expected code over the %d questions every arm ranks", len(common))
	for i, a := range arms {
		t.Logf("  %-44s %.2f", a.name, meanRankOver(ranks[i], common))
	}
}
