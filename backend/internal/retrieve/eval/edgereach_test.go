package eval

import (
	"context"
	"testing"

	"github.com/trick77/rongo/internal/edges"
)

// TestFlowEdgeReach asks the question the edge table exists to answer: standing
// on ONE part of a flow, does an integration edge reach the others?
//
// It is deliberately independent of the loop diagnostic. The loop's misses are
// a property of a model on a day; this is a property of the corpus and the
// table, and it says how much of each flow the edges could hand over without
// the model having to decide to look.
//
// One hop only. Two hops would flatter the table: with enough hops everything
// reaches everything, and the number would stop meaning anything.
func TestFlowEdgeReach(t *testing.T) {
	requireEval(t)
	db := evalDB(t, embedDim(t))
	ctx := context.Background()

	questions := loadFlowQuestions(t)
	var totalPairs, totalReached, totalRepoReached, totalComposed int

	for _, q := range questions {
		parts := q.parts()
		if len(parts) < 2 {
			continue
		}
		reached := map[flowPart]bool{}
		// repoReached is the weaker, and more honest, granularity: an edge that
		// lands anywhere in the right repository has told retrieval which
		// repository to work in, and the in-repo walk already does the rest.
		repoReached := map[flowPart]bool{}
		// composed is the same measurement through Reach: in-repo hop, edge,
		// in-repo hop. Both are reported so the walk's contribution is visible
		// rather than asserted.
		composed := map[flowPart]bool{}
		for _, from := range parts {
			ns, err := edges.Neighbours(ctx, db, from.Repo, from.Path)
			if err != nil {
				t.Fatalf("Neighbours(%s): %v", from, err)
			}
			for _, n := range ns {
				for _, to := range parts {
					if to == from {
						continue
					}
					if to.Repo == n.Repo && to.Path == n.Path {
						reached[to] = true
					}
					if to.Repo == n.Repo {
						repoReached[to] = true
					}
				}
			}
			rs, err := edges.Reach(ctx, db, from.Repo, from.Path)
			if err != nil {
				t.Fatalf("Reach(%s): %v", from, err)
			}
			for _, r := range rs {
				for _, to := range parts {
					if to != from && to.Repo == r.Repo && to.Path == r.Path && to.Repo != from.Repo {
						composed[to] = true
					}
				}
			}
		}
		// A part is counted once, however many other parts reach it.
		crossRepo := 0
		for _, p := range parts {
			for _, other := range parts {
				if other.Repo != p.Repo {
					crossRepo++
					break
				}
			}
		}
		totalPairs += crossRepo
		totalReached += len(reached)
		totalRepoReached += len(repoReached)
		totalComposed += len(composed)
		t.Logf("%-64.64s edge %d/%d, repo %d/%d, composed %d/%d", q.Text,
			len(reached), crossRepo, len(repoReached), crossRepo, len(composed), crossRepo)
		for _, p := range parts {
			if !composed[p] {
				t.Logf("    still unreached: %s", p)
			}
		}
	}
	t.Logf("\nacross the catalogue: edge alone %d/%d exact, %d/%d by repository; "+
		"composed walk %d/%d exact",
		totalReached, totalPairs, totalRepoReached, totalPairs, totalComposed, totalPairs)
}
