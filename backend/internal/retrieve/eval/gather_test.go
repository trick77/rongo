package eval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/retrieve"
)

// gatherSearchK mirrors ask.searchK, which is unexported. Deeper than the ten a
// person would read, because the reference walk uses the tail: a handler that
// ranks twelfth is still the thread that leads to the service.
const gatherSearchK = 20

// gatherOpts reads the same bounds the running service uses, so the measurement
// describes the deployed configuration rather than one invented here.
func gatherOpts(t *testing.T) ask.GatherOptions {
	t.Helper()
	return ask.GatherOptions{
		MaxHops:     envIntOr(t, "BACKEND_GATHER_MAX_HOPS", 2),
		TokenBudget: envIntOr(t, "BACKEND_GATHER_TOKEN_BUDGET", 24000),
	}
}

// hopOfCandidate returns the hop at which one of this candidate's files entered
// the gathered set, or -1. Hop 0 means the search already had it; anything
// higher means the reference walk reached it.
func hopOfCandidate(sources []ask.Source, c Candidate) int {
	best := -1
	for _, s := range sources {
		if s.Repo != c.Repo {
			continue
		}
		for _, want := range c.Paths {
			if s.Path == want && (best < 0 || s.Hop < best) {
				best = s.Hop
			}
		}
	}
	return best
}

// gatherArm is one configuration of the pipeline's first three steps.
type gatherArm struct {
	name string
	// expanded searches the frozen expansion instead of the raw question. The
	// running pipeline always does; the existing harnesses never do.
	expanded bool
	hops     int
}

// gatherOutcome is one question under one arm.
type gatherOutcome struct {
	q       Question
	rank    int   // rank of the first candidate in the SEARCH result
	hops    []int // per candidate, the hop it was gathered at, or -1
	ranks   []int // per candidate, its own rank in the SEARCH result, or 0
	sources int
}

func (o gatherOutcome) found() int {
	n := 0
	for _, h := range o.hops {
		if h >= 0 {
			n++
		}
	}
	return n
}

// byWalk reports whether the walk is what reached the first candidate — the
// distinction the whole measurement exists for.
func (o gatherOutcome) byWalk() bool {
	best := -1
	for _, h := range o.hops {
		if h >= 0 && (best < 0 || h < best) {
			best = h
		}
	}
	return best > 0
}

// TestEvalMeasureGathered scores what actually reaches the answer.
//
// Both existing harnesses stop at retrieve.Search, and the product does not:
// ask.Pipeline searches, then expands the hits by following symbol references
// out of them, and the answer is written from that gathered set.
// peeq's playbackgrant/store.go is the standing example — it misses the search
// at K=20 and is nonetheless cited correctly in the real answer.
//
// This reports BESIDE the existing recall numbers, never instead of them. The
// two answer different questions, and the measurement documents compare against
// the old ones.
//
// The named attack is an arm that cannot fail: with two hops and a 24000-token
// budget the gatherer may pull in so much of the corpus that everything looks
// found. Two things guard against reading that as a result — the mean source
// count is printed next to every recall, and the first arm walks zero hops.
// That arm returns exactly the search hits, so it MUST reproduce the recall@20
// of TestEvalMeasure. A mismatch there is a wiring bug in this harness, not a
// finding.
func TestEvalMeasureGathered(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()

	client := embed.NewClient(embed.Config{
		BaseURL: os.Getenv("BACKEND_EMBED_BASE_URL"),
		APIKey:  os.Getenv("BACKEND_EMBED_API_KEY"),
		Model:   envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"),
		Dim:     dim,
	}, nil)
	r := retrieve.New(db, client)
	questions := loadQuestions(t)
	expansions := loadExpansions(t)
	opts := gatherOpts(t)

	arms := []gatherArm{
		{name: "raw, 0 hops", expanded: false, hops: 0},
		{name: "raw + walk", expanded: false, hops: opts.MaxHops},
		{name: "expanded + walk", expanded: true, hops: opts.MaxHops},
	}

	t.Logf("questions=%d max_hops=%d token_budget=%d", len(questions), opts.MaxHops, opts.TokenBudget)

	results := map[string][]gatherOutcome{}
	for _, arm := range arms {
		g := ask.NewGatherer(db, ask.GatherOptions{MaxHops: arm.hops, TokenBudget: opts.TokenBudget})
		var out []gatherOutcome
		for _, q := range questions {
			query := retrieve.Query{Text: q.Text, Question: q.Text, K: gatherSearchK}
			if arm.expanded {
				texts, ok := expansions[q.Text]
				if !ok {
					t.Fatalf("no expansion recorded for %q — run TestExpandQuestions first", q.Text)
				}
				query = retrieve.Query{Texts: texts, Question: q.Text, K: gatherSearchK}
			}
			hits, err := r.Search(ctx, query)
			if err != nil {
				t.Fatalf("%s: search %q: %v", arm.name, q.Text, err)
			}
			sources, err := g.Gather(ctx, hits)
			if err != nil {
				t.Fatalf("%s: gather %q: %v", arm.name, q.Text, err)
			}
			hops := make([]int, 0, len(q.Candidates))
			ranks := make([]int, 0, len(q.Candidates))
			for _, c := range q.Candidates {
				hops = append(hops, hopOfCandidate(sources, c))
				// The candidate's own rank in the SEARCH hits, beside the hop
				// it reached the sources at. One number cannot tell a part
				// retrieval never found from one it found and the walk did not
				// need; two can.
				ranks = append(ranks, rankOfCandidate(hits, c))
			}
			out = append(out, gatherOutcome{
				q: q, rank: rankOfExpected(hits, q), hops: hops, ranks: ranks, sources: len(sources),
			})
		}
		results[arm.name] = out
		reportArm(t, arm.name, out)
	}

	reportWalkGains(t, results["expanded + walk"])
	reportCompositionParts(t, results["expanded + walk"])
}

// reportCompositionParts answers the one question the aggregate cannot: when a
// composition question does not get all its parts, was the missing part never
// RETRIEVED, or was it retrieved and the walk simply did not need to reach it?
//
// The two have nothing in common as defects. A part that never appears in the
// search result is a retrieval problem, and the only cohort in this catalogue
// where a wider search or a second query could still recover something
// (2026-09-06-routing-cost-metric.md closed the others: grounding on answered
// unique questions is 1.000). A part that ranks inside the hits but never
// reaches the sources is a gathering problem, and the hop budget or the
// eviction rule owns it.
//
// Per part: its rank in the search hits, and the hop it was gathered at.
//
//	rank 0, hop -1   never retrieved, never walked to — a SEARCH miss
//	rank n, hop -1   retrieved and then lost — a GATHER miss
//	rank n, hop 0    arrived as a search hit
//	rank 0, hop >0   the walk rescued it
func reportCompositionParts(t *testing.T, out []gatherOutcome) {
	t.Helper()
	var searchMiss, gatherMiss, viaSearch, viaWalk int

	t.Logf("")
	t.Logf("=== composition, part by part (expanded + walk) ===")
	t.Logf("%-6s %-6s %-9s %-11s %s", "rank", "hop", "verdict", "repo", "path")
	for _, o := range out {
		if o.q.Resolution != ResolutionComposition {
			continue
		}
		t.Logf("· %s", short(o.q.Text))
		for i, c := range o.q.Candidates {
			rank, hop := 0, -1
			if i < len(o.ranks) {
				rank = o.ranks[i]
			}
			if i < len(o.hops) {
				hop = o.hops[i]
			}
			verdict := ""
			switch {
			case hop < 0 && rank == 0:
				verdict = "SEARCH"
				searchMiss++
			case hop < 0:
				verdict = "GATHER"
				gatherMiss++
			case hop == 0:
				verdict = "search"
				viaSearch++
			default:
				verdict = "walk"
				viaWalk++
			}
			path := ""
			if len(c.Paths) > 0 {
				path = c.Paths[0]
			}
			t.Logf("  %-6d %-6d %-9s %-11s %s", rank, hop, verdict, c.Repo, path)
		}
	}

	t.Logf("")
	t.Logf("parts arrived: %d by search, %d by the walk", viaSearch, viaWalk)
	t.Logf("parts missing: %d never retrieved (SEARCH), %d retrieved but not gathered (GATHER)",
		searchMiss, gatherMiss)
}

// reportArm prints one arm's cohort metrics.
//
// The cohorts are reported separately on purpose. The headline recall of the
// existing documents is computed over questions with ONE right answer, and
// mixing the other two in would silently change what those numbers mean.
func reportArm(t *testing.T, name string, out []gatherOutcome) {
	t.Helper()

	var uniqueN, uniqueFound, uniqueByWalk int
	var ambigN, ambigBothPlus, ambigCandidates, ambigFound int
	var compN, compComplete, compParts, compFound int
	totalSources := 0

	for _, o := range out {
		totalSources += o.sources
		switch o.q.Resolution {
		case ResolutionUnique:
			uniqueN++
			if o.found() > 0 {
				uniqueFound++
				if o.byWalk() {
					uniqueByWalk++
				}
			}
		case ResolutionAmbiguous:
			ambigN++
			ambigCandidates += len(o.q.Candidates)
			ambigFound += o.found()
			if o.found() >= 2 {
				ambigBothPlus++
			}
		case ResolutionComposition:
			compN++
			compParts += len(o.q.Candidates)
			compFound += o.found()
			if o.found() == len(o.q.Candidates) {
				compComplete++
			}
		}
	}

	t.Logf("")
	t.Logf("=== %s ===", name)
	t.Logf("mean sources/question = %.1f", float64(totalSources)/float64(max(len(out), 1)))
	if uniqueN > 0 {
		t.Logf("unique      gathered recall = %.3f (%d/%d), of which reached by the walk: %d",
			float64(uniqueFound)/float64(uniqueN), uniqueFound, uniqueN, uniqueByWalk)
	}
	if ambigN > 0 {
		// A clarification needs at least two alternatives on the table. One
		// alternative gathered is an answer that looks confident and is a coin
		// flip.
		t.Logf("ambiguous   two or more alternatives gathered = %.3f (%d/%d), candidates %d/%d",
			float64(ambigBothPlus)/float64(ambigN), ambigBothPlus, ambigN, ambigFound, ambigCandidates)
	}
	if compN > 0 {
		t.Logf("composition all parts gathered = %.3f (%d/%d), parts %d/%d",
			float64(compComplete)/float64(compN), compComplete, compN, compFound, compParts)
	}
}

// reportWalkGains lists the questions the reference walk rescued, and the ones
// still missing after it. Those two lists are the material a decision is made
// from; the aggregate above only says how large they are.
func reportWalkGains(t *testing.T, out []gatherOutcome) {
	t.Helper()
	sorted := append([]gatherOutcome(nil), out...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return rankOrLast(sorted[i].rank) > rankOrLast(sorted[j].rank)
	})

	t.Logf("")
	t.Logf("%-6s %-8s %-8s %-6s %s", "rank", "gathered", "hops", "srcs", "question")
	for _, o := range sorted {
		rank := "MISS"
		if o.rank > 0 {
			rank = fmt.Sprintf("%d", o.rank)
		}
		state := "no"
		if o.found() > 0 {
			state = fmt.Sprintf("%d/%d", o.found(), len(o.q.Candidates))
		}
		t.Logf("%-6s %-8s %-8v %-6d %s [%s]", rank, state, o.hops, o.sources,
			short(o.q.Text), o.q.Resolution)
	}
}
