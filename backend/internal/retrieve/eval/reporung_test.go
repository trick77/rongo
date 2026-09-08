package eval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
)

// TestEvalMeasureRepoRungShape is a DIAGNOSTIC, not a scored arm. It exists
// because 2026-09-06 attributed all 16 of the ladder's false cards to the
// repository rung, and the obvious remedy — "make the spanning candidate clear
// worthOffering" — is already in force: Ranked.All is the filtered list, so
// the 0.4 relative floor has run before distinctRepos ever counts a second
// repository. Whatever separates the 16 wrong cards from the 14 right ones is
// therefore something this dump has to find, not something to guess at.
//
// It calls no model. Rank is pure over the hits and Related is a manifest
// query, so the only cost is the embeddings the frozen expansions need.
//
// Related is not optional here even though it costs a query: the ladder checks
// the manifest edge BEFORE the repository rung (see DecideWhy), so a spanning
// turn whose repositories are related never reaches the rung at all. Counting
// every spanning turn as a card the rung raised mixes those in and measures a
// population the rung does not decide.
func TestEvalMeasureRepoRungShape(t *testing.T) {
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
	r := retrieve.New(db, embedder)
	expansions := loadExpansions(t)
	expansionRepos := loadExpansionRepos(t)
	margin := routeMargin(t)
	router := ask.NewRouter(nil, db, margin, moduleOpts(t))

	type row struct {
		q        Question
		want     bool
		spans    bool
		repos    int
		cands    int
		lead     float64
		leadRepo string
		second   float64
		secRepo  string
		ratio    float64 // second repository's best, as a share of the leader
		secHits  int     // hits behind the second repository's best candidate
		dominant bool
		related  bool
		// decides is whether the repository rung is what settles this turn:
		// spanning, not already composed by the manifest edge, and inside the
		// cap that would make it a too-broad card instead.
		decides bool
	}

	var rows []row
	for _, q := range loadQuestions(t) {
		hits, named := hitsFor(t, ctx, r, expansions, expansionRepos, q)
		if len(named) >= 1 {
			continue // the named-repo rung settles these before the rung under test
		}
		ranked, err := router.Rank(ctx, hits)
		if err != nil {
			t.Fatalf("rank %q: %v", q.Text, err)
		}
		if len(ranked.All) == 0 {
			continue
		}
		// Best candidate per repository, in rank order.
		var perRepo []ask.Candidate
		seen := map[string]bool{}
		for _, c := range ranked.All {
			if seen[c.Repo] {
				continue
			}
			seen[c.Repo] = true
			perRepo = append(perRepo, c)
		}
		rw := row{
			q:        q,
			want:     resolutionExpectsAsk(q.Resolution),
			spans:    ask.SpansRepos(ranked.All, len(named), projects.Map{}),
			repos:    len(perRepo),
			cands:    len(ranked.All),
			lead:     ranked.All[0].Score,
			leadRepo: ranked.All[0].Repo,
			dominant: ask.Dominates(ranked.All, margin),
		}
		if rw.spans {
			rw.related, err = router.Related(ctx, ask.RepoCandidates(ranked.All))
			if err != nil {
				t.Fatalf("related %q: %v", q.Text, err)
			}
			rw.decides = !rw.related && len(perRepo) <= ask.MaxRepoCandidates
		}
		if len(perRepo) >= 2 {
			rw.second = perRepo[1].Score
			rw.secRepo = perRepo[1].Repo
			rw.secHits = len(perRepo[1].Hits)
			if rw.lead > 0 {
				rw.ratio = rw.second / rw.lead
			}
		}
		rows = append(rows, rw)
	}

	// The turns the rung actually decides — spanning, and not settled above it
	// by the manifest edge or the too-broad cut.
	var spanning []row
	var spansButComposed int
	for _, rw := range rows {
		switch {
		case rw.decides:
			spanning = append(spanning, rw)
		case rw.spans:
			spansButComposed++
		}
	}
	sort.SliceStable(spanning, func(i, j int) bool { return spanning[i].ratio < spanning[j].ratio })

	t.Logf("turns the repository rung decides=%d of %d unnamed questions (%d span but are settled above it)",
		len(spanning), len(rows), spansButComposed)
	t.Logf("")
	t.Logf("%-6s %-6s %-6s %-6s %-7s %-7s %-5s %-11s %-11s %-12s %s",
		"want", "repos", "cands", "domin", "ratio", "second", "sHits", "leadRepo", "secRepo",
		"resolution", "question")
	for _, rw := range spanning {
		t.Logf("%-6v %-6d %-6d %-6v %-7.3f %-7.3f %-5d %-11s %-11s %-12s %s",
			rw.want, rw.repos, rw.cands, rw.dominant, rw.ratio, rw.second, rw.secHits,
			rw.leadRepo, rw.secRepo, rw.q.Resolution, short(rw.q.Text))
	}

	// The separation question. A turn that fails the bar does NOT become an
	// answer: spans goes false and the ladder falls through to Dominates and,
	// failing that, to the judge — which was 0-for-wrong on this corpus. So
	// every sweep reports where the dropped turns land, and "net" is an upper
	// bound that only holds if the judge keeps behaving.
	baseCards := len(spanning)
	baseRight := 0
	for _, rw := range spanning {
		if rw.want {
			baseRight++
		}
	}
	baseWrong := baseCards - baseRight

	report := func(label string, keep func(row) bool) {
		kept, right, lostAmbig, toAnswer, toJudge := 0, 0, 0, 0, 0
		for _, rw := range spanning {
			if keep(rw) {
				kept++
				if rw.want {
					right++
				}
				continue
			}
			if rw.want {
				lostAmbig++
			}
			if rw.dominant {
				toAnswer++
			} else {
				toJudge++
			}
		}
		wrong := kept - right
		// Against the baseline this run measured, never a constant: on another
		// question set or after a corpus move a hardcoded pair reports a
		// difference from a population that is not there any more.
		t.Logf("%-22s %-6d %-7d %-7d %-9d %-8d %-8d %+d",
			label, kept, right, wrong, lostAmbig, toAnswer, toJudge,
			(baseWrong-wrong)-(baseRight-right))
	}

	t.Logf("")
	t.Logf("%-22s %-6s %-7s %-7s %-9s %-8s %-8s %s",
		"condition", "cards", "right", "wrong", "ambig-", "->answer", "->judge", "net")
	report("baseline", func(row) bool { return true })
	for _, bar := range []float64{0.50, 0.60, 0.70, 0.75, 0.80, 0.85, 0.90} {
		b := bar
		report(fmt.Sprintf("ratio >= %.2f", b), func(rw row) bool { return rw.ratio >= b })
	}
	for _, min := range []int{2, 3, 4} {
		m := min
		report(fmt.Sprintf("sHits >= %d", m), func(rw row) bool { return rw.secHits >= m })
	}
	for _, min := range []int{2, 3, 4} {
		for _, bar := range []float64{0.60, 0.75, 0.85} {
			m, b := min, bar
			report(fmt.Sprintf("sHits>=%d & r>=%.2f", m, b), func(rw row) bool { return rw.secHits >= m && rw.ratio >= b })
		}
	}
}
