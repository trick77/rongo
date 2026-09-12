package eval

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/projects"
	"github.com/trick77/rongo/internal/retrieve"
)

// routeSearchK mirrors ask.searchK (unexported), the same way gatherSearchK in
// gather_test.go does: the running pipeline searches 20 hits before routing.
const routeSearchK = 20

// routeMargins is the sweep the phase 4b spec asks for. The accuracy table it
// produces does NOT choose BACKEND_ROUTE_MARGIN's value, and on the corpus of
// 2026-08-20 it cannot: the table is flat. 0.667 at 0.10 and 0.633 to 0.650
// everywhere from 0.15 to 0.40 — the whole useful range of the threshold is
// worth at most two questions in sixty.
//
// That flatness is a finding rather than a disappointment. The threshold's job
// is to arbitrate between two close candidates, and only 7 of the 16 ambiguous
// questions retrieve both of their answers into the top 20 at all, so there is
// usually no second candidate to arbitrate. The default stays at 0.25 because
// nothing in this table argues for moving it. See
// docs/measurements/2026-08-20-corpus-swap.md, "Why routing collapsed".
var routeMargins = []float64{0.10, 0.15, 0.20, 0.25, 0.30, 0.40}

// resolutionExpectsAsk is the criterion fixed before any run: an ambiguous
// question is served correctly only by a clarification, and a unique or
// composition question is served correctly by an answer — never a question.
func resolutionExpectsAsk(res Resolution) bool {
	return res == ResolutionAmbiguous
}

// The ask/answer decision itself is ask.Decide — the same function Route
// calls, never a copy of it here. This file only decides which rungs to PAY
// for: the grouped candidates (Rank, no database or model call), the
// manifest-dependency check (Related, run at most once regardless of how many
// margins are swept), and — only if the ladder needs it at all — the judge's
// single answer. Those three are then handed to ask.Decide once per margin,
// so a six-margin sweep costs one Related and one Judge, not six.

// anyMarginNeedsLadder reports whether at least one margin in margins fails
// to dominate this ranking — i.e. whether the ladder needs to go on past
// Dominates (into Related, and possibly Judge) for ANY of margins. Checking
// every margin, not just the loosest or the strictest, is what makes this
// correct regardless of how the sweep's margins are chosen: Dominates is not
// guaranteed monotonic in margin for every possible sweep shape.
func anyMarginNeedsLadder(all []ask.Candidate, margins []float64) bool {
	for _, m := range margins {
		if !ask.Dominates(all, m) {
			return true
		}
	}
	return false
}

// rankRoute reproduces Router.Route's ladder ONCE per question, for every
// margin in margins at once: Rank always runs (cheap, in-memory); Related — an
// O(n^2) set of database queries — and Judge — a paid model call — run only
// when at least one margin in margins would actually reach that rung, exactly
// as Route()'s own short-circuit does. The three returned values are then fed
// into ask.Decide for as many margins as the caller wants, at no further database
// or model cost.
//
// named is how many repositories the question named that the index carries.
// It is not there to be re-decided here — ask.Decide does that — but because
// the repository rung is NOT keyed off the margin: a question that names no
// repository and whose candidates span several reaches Related whatever the
// margin says, and asks it about the repositories rather than the capped
// module list. A harness that kept paying for Related on the margin alone
// would hand Decide related=false on exactly those turns and measure a card
// where the product composes.
func rankRoute(ctx context.Context, t *testing.T, r *ask.Router, question string, hits []retrieve.Hit, margins []float64, named int) (all []ask.Candidate, related, judged bool) {
	t.Helper()
	ranked, err := r.Rank(ctx, hits)
	if err != nil {
		t.Fatalf("rank %q: %v", question, err)
	}
	all = ranked.All
	spans := ask.SpansRepos(all, named, projects.Map{})
	if !spans && !anyMarginNeedsLadder(all, margins) {
		return all, false, false
	}
	depsOver := ranked.Capped
	if spans {
		depsOver = ask.RepoCandidates(all)
	}
	related, err = r.Related(ctx, depsOver)
	if err != nil {
		t.Fatalf("related %q: %v", question, err)
	}
	if related {
		return all, true, false
	}
	if spans {
		// The repository rung decides on its own; Route never pays the judge
		// here, so neither does the sweep.
		return all, false, false
	}
	judged, err = r.Judge(ctx, question, ranked.Capped)
	if err != nil {
		t.Fatalf("judge %q: %v", question, err)
	}
	return all, false, judged
}

// llmClientForRouting builds the model client the routing arms share. Skips —
// never fails — when no endpoint is configured, exactly like
// TestExpandQuestions: a routing arm without BACKEND_CHAT_BASE_URL cannot call
// the judge at all.
func llmClientForRouting(t *testing.T) *llm.Client {
	t.Helper()
	return evalLLM(t, 2*time.Minute)
}

// routeMargin reads BACKEND_ROUTE_MARGIN the same way config.go does, so the
// ShortGate/Pro comparison reports on the deployed threshold rather than one
// invented here. The margin sweep further down is what the threshold itself
// is measured against; this is only where the comparison arm runs.
func routeMargin(t *testing.T) float64 {
	t.Helper()
	v := envOr("BACKEND_ROUTE_MARGIN", "0.25")
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatalf("BACKEND_ROUTE_MARGIN = %q, want a number", v)
	}
	return f
}

// hitsFor runs the frozen expansion for q through Search, exactly as
// pipeline.go:93 does — BOTH of the understanding's outputs, the texts and
// the repo restriction. Passing only the texts is what these arms did before
// phase 4c, and it made them measure a router the product does not run: a
// question that names a system never sees the other repositories in
// production, so a candidate set drawn from all of them produced
// clarifications nobody could ever have been asked.
//
// Reusing expansions.json is the same reuse gather_test.go relies on: a
// routing measurement should not also pay for a fresh understanding call per
// question, and freezing is what makes the measurement repeatable.
// It also returns the named repositories the index actually carries, because
// that is an input to ask.Decide and the harness has to hand Decide the same
// thing the product does — a harness that passed nothing here would measure a
// router without the comparison rung, which is the defect the exported ladder
// exists to prevent.
//
// The search itself mirrors Pipeline.searchScoped: two or more named
// repositories are searched one at a time, so neither side can be crowded out
// of the cut by the other.
func hitsFor(t *testing.T, ctx context.Context, r *retrieve.Retriever, expansions, repos map[string][]string, q Question) ([]retrieve.Hit, []string) {
	t.Helper()
	texts, ok := expansions[q.Text]
	if !ok {
		t.Fatalf("no expansion recorded for %q — run TestExpandQuestions first", q.Text)
	}
	known, _, err := r.ResolveRepos(ctx, repos[q.Text], q.Text)
	if err != nil {
		t.Fatalf("resolve repos %q: %v", q.Text, err)
	}
	if len(known) < 2 {
		hits, err := r.Search(ctx, retrieve.Query{Texts: texts, Repos: known, Question: q.Text, K: routeSearchK})
		if err != nil {
			t.Fatalf("search %q: %v", q.Text, err)
		}
		return hits, known
	}
	var all []retrieve.Hit
	for _, repo := range known {
		hits, err := r.Search(ctx, retrieve.Query{Texts: texts, Repos: []string{repo}, K: routeSearchK})
		if err != nil {
			t.Fatalf("search %q in %s: %v", q.Text, repo, err)
		}
		all = append(all, hits...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	return all, known
}

// routingRow is one question's outcome under one routing arm, kept for the
// per-question detail printed after the aggregate.
type routingRow struct {
	q    Question
	want bool
	got  bool
	// rung is the ladder step that settled this question, as DecideWhy names
	// it. Production logs it per turn for exactly this reason (route.go's
	// ladder line): an aggregate says how often routing was wrong, and only
	// the rung says which rule to go and fix. Without it a run cannot tell a
	// deterministic repository card apart from a judgement the model made.
	rung string
}

func (r routingRow) correct() bool { return r.got == r.want }

// composeCosts are the prices this harness puts on a wrongly composed answer,
// with a needless card fixed at 1. They are a SWEEP and not a constant on
// purpose: nobody can defend a single number here, but the ranking of the
// settings turns out not to need one — see the crossover line below.
var composeCosts = []float64{1, 1.5, 2, 3}

// reportRoutingCost prices the two ways routing can be wrong apart, which the
// accuracy above does not.
//
// Accuracy counts a needless card and a wrongly composed answer as one error
// each. The product says they are not the same thing, in the judge's own
// prompt: "a follow-up question costs one click, an answer composed across
// independent mechanisms is simply wrong" (ask/route.go, judgeSystem). An
// unweighted aggregate therefore rewards silence — 49 of the 65 questions want
// no card, so a router that never asks scores 0.754 while being useless — and
// every threshold tuned against it is pulled toward asking less.
//
// So: cost = (needless cards) + W * (missed ambiguities), swept over W, plus
// the W at which this setting overtakes never-asking. A setting whose crossover
// is below 1 is better than silence even under the metric that flatters
// silence; one at 1.14 needs a reader to think a wrong compose is 14% worse
// than a wasted click.
func reportRoutingCost(t *testing.T, cards, missed, ambigN int) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "  cost: %d needless card(s), %d missed ambiguit(y|ies) →", cards, missed)
	for _, w := range composeCosts {
		fmt.Fprintf(&b, "  W=%.1f: %.1f", w, float64(cards)+w*float64(missed))
	}
	t.Log(b.String())

	// Never asking costs every ambiguous question and no cards, so it is
	// ambigN*W. This setting overtakes it where cards + W*missed = W*ambigN.
	if d := ambigN - missed; d > 0 {
		t.Logf("  beats never-ask once a wrong compose costs more than %.2f needless cards",
			float64(cards)/float64(d))
	} else {
		t.Logf("  never beats never-ask: it misses as many ambiguous questions and pays cards on top")
	}
}

// reportRouting prints one arm's accuracy, overall and split by resolution —
// the split matters because the two ways to be WRONG are opposite mistakes:
// asking about a unique/composition/comparison question annoys the reader,
// answering an ambiguous one silently guesses. reportRoutingCost then prices
// that difference, which the accuracy line deliberately does not.
func reportRouting(t *testing.T, label string, rows []routingRow) {
	t.Helper()
	var correct, ambigCorrect, ambigN, otherCorrect, otherN int
	for _, row := range rows {
		if row.correct() {
			correct++
		}
		if row.want {
			ambigN++
			if row.correct() {
				ambigCorrect++
			}
		} else {
			otherN++
			if row.correct() {
				otherCorrect++
			}
		}
	}
	n := len(rows)
	t.Logf("--- %s (n=%d)", label, n)
	t.Logf("overall accuracy = %.3f (%d/%d)", float64(correct)/float64(n), correct, n)
	if ambigN > 0 {
		t.Logf("  ambiguous (want Ask=true)             = %.3f (%d/%d)",
			float64(ambigCorrect)/float64(ambigN), ambigCorrect, ambigN)
	}
	if otherN > 0 {
		t.Logf("  unique+composition+comparison (Ask=false) = %.3f (%d/%d)",
			float64(otherCorrect)/float64(otherN), otherCorrect, otherN)
	}
	reportRoutingCost(t, otherN-otherCorrect, ambigN-ambigCorrect, ambigN)

	sorted := append([]routingRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		// Wrong first, then by question text, so a misroute never scrolls off
		// the bottom of a long run.
		if sorted[i].correct() != sorted[j].correct() {
			return !sorted[i].correct()
		}
		return sorted[i].q.Text < sorted[j].q.Text
	})
	// Which rung settled the questions this arm got WRONG. The aggregate says
	// how much routing costs; this says which rule to change to get it back.
	byRung := map[string]int{}
	for _, row := range rows {
		if !row.correct() {
			byRung[row.rung]++
		}
	}
	rungs := make([]string, 0, len(byRung))
	for rung := range byRung {
		rungs = append(rungs, rung)
	}
	sort.SliceStable(rungs, func(i, j int) bool { return byRung[rungs[i]] > byRung[rungs[j]] })
	for _, rung := range rungs {
		t.Logf("  wrong via rung %-12s = %d", rung, byRung[rung])
	}

	t.Logf("%-5s %-5s %-5s %-12s %-12s %s", "want", "got", "ok", "rung", "resolution", "question")
	for _, row := range sorted {
		t.Logf("%-5v %-5v %-5v %-12s %-12s %s", row.want, row.got, row.correct(), row.rung, row.q.Resolution, short(row.q.Text))
	}
}

// TestEvalMeasureRouting measures the routing decision on both judge
// deployments, over all 65 questions. The comparison is standing work, not a
// one-off: phase 4b made it mandatory before non-Pro could be written in,
// phase 4c overturned its result, and 2026-09-06 overturned that one and moved
// the judge back to the cheap lane. Whichever lane NewRouter builds, this arm
// keeps measuring the other — which is why both arms name their deployment
// rather than letting one inherit the default.
//
// Both arms share one Rank per question (no margin dependency, no database
// or model call) and call Related/Judge only when the ladder would actually
// reach them — never Route()'s naming step, which nothing here reads.
func TestEvalMeasureRouting(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	client := llmClientForRouting(t)

	embedder := evalEmbedder(t, envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"), dim)
	r := retrieve.New(db, embedder)
	expansions := loadExpansions(t)
	expansionRepos := loadExpansionRepos(t)
	questions := loadQuestions(t)
	margin := routeMargin(t)
	mo := moduleOpts(t)

	// ShortGate is what NewRouter builds now — 2026-09-06 measured the two
	// deployments deciding every question identically and moved the judge back
	// to the cheap lane, see the comment on Router.judgeDeployment. BOTH arms
	// name their deployment rather than letting one of them inherit the
	// default: an arm labelled "Pro" that silently follows NewRouter would
	// compare the default against itself the next time the default moves, and
	// report it as agreement.
	pro := ask.NewRouter(client, db, margin, mo).WithJudgeDeployment(llm.Pro())
	shortGate := ask.NewRouter(client, db, margin, mo).WithJudgeDeployment(llm.ShortGate())

	t.Logf("questions=%d margin=%.2f", len(questions), margin)

	var shortRows, proRows []routingRow
	for _, q := range questions {
		hits, named := hitsFor(t, ctx, r, expansions, expansionRepos, q)
		want := resolutionExpectsAsk(q.Resolution)

		sAll, sRelated, sJudged := rankRoute(ctx, t, shortGate, q.Text, hits, []float64{margin}, len(named))
		sGot, sRung := ask.DecideWhy(sAll, margin, sRelated, sJudged, len(named), false, true, projects.Map{})
		shortRows = append(shortRows, routingRow{q: q, want: want, got: sGot, rung: sRung})

		pAll, pRelated, pJudged := rankRoute(ctx, t, pro, q.Text, hits, []float64{margin}, len(named))
		pGot, pRung := ask.DecideWhy(pAll, margin, pRelated, pJudged, len(named), false, true, projects.Map{})
		proRows = append(proRows, routingRow{q: q, want: want, got: pGot, rung: pRung})
	}

	t.Logf("")
	reportRouting(t, "judge on ShortGate (non-Pro)", shortRows)
	t.Logf("")
	reportRouting(t, "judge on Pro", proRows)
}

// TestEvalMeasureRoutingMarginSweep reports routing accuracy at every margin
// in routeMargins, over all 65 questions, on the judge production actually
// runs — the cheap lane since 2026-09-06. The chosen constant comes out of
// this table; the table does not assume it. Sweeping the other deployment
// would tune a threshold against a router nobody serves.
//
// Rank, Related and Judge each run AT MOST ONCE per question and are reused
// across every margin via ask.Decide — the sweep does not re-pay for the
// dependency query or the judge call six times over.
func TestEvalMeasureRoutingMarginSweep(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	client := llmClientForRouting(t)

	embedder := evalEmbedder(t, envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"), dim)
	r := retrieve.New(db, embedder)
	expansions := loadExpansions(t)
	expansionRepos := loadExpansionRepos(t)
	questions := loadQuestions(t)
	mo := moduleOpts(t)

	// The margin passed to NewRouter here is never read: this arm calls Rank,
	// Related and Judge directly, bypassing Route()'s own margin check so
	// every margin in the sweep can be tried against the SAME ranking and the
	// SAME judgement. 0 makes that plain rather than implying a margin
	// matters.
	router := ask.NewRouter(client, db, 0, mo)

	t.Logf("questions=%d margins=%v", len(questions), routeMargins)

	type perQuestion struct {
		all     []ask.Candidate
		related bool
		judged  bool
		named   int
		want    bool
		q       Question
	}
	rows := make([]perQuestion, 0, len(questions))
	for _, q := range questions {
		hits, named := hitsFor(t, ctx, r, expansions, expansionRepos, q)
		all, related, judged := rankRoute(ctx, t, router, q.Text, hits, routeMargins, len(named))
		rows = append(rows, perQuestion{all: all, related: related, judged: judged, named: len(named), want: resolutionExpectsAsk(q.Resolution), q: q})
	}

	t.Logf("")
	t.Logf("%-8s %-10s %s", "margin", "accuracy", "correct/total")
	for _, margin := range routeMargins {
		correct := 0
		for _, row := range rows {
			if ask.Decide(row.all, margin, row.related, row.judged, row.named, false, true, projects.Map{}) == row.want {
				correct++
			}
		}
		t.Logf("%-8.2f %-10.3f %d/%d", margin, float64(correct)/float64(len(rows)), correct, len(rows))
	}
}

// TestEvalMeasureRoutingGrounding checks that routing narrows nothing: on the
// 44 unique questions, after understand, search, route and gather, the
// expected file must still be among the sources the answer would be built
// from. The number to beat is the published 0.955 (phase 4a's gathered
// recall on the same cohort, without a routing step in front of it) — a drop
// means routing narrowed what the answer is built from, which the design
// forbids.
//
// The 0.955 comparison and this arm's aggregate over all 44 are NOT the same
// question if the router ever asks about a unique question: an asked turn
// never gathers, which counts as ungrounded here and is a routing failure,
// not a gathering one. Both numbers are reported so a drop can be attributed
// to the right step instead of being read off one aggregate that mixes them.
func TestEvalMeasureRoutingGrounding(t *testing.T) {
	requireEval(t)
	dim := embedDim(t)
	db := evalDB(t, dim)
	ctx := context.Background()
	client := llmClientForRouting(t)

	embedder := evalEmbedder(t, envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"), dim)
	r := retrieve.New(db, embedder)
	expansions := loadExpansions(t)
	expansionRepos := loadExpansionRepos(t)
	mo := moduleOpts(t)
	margin := routeMargin(t)
	router := ask.NewRouter(client, db, margin, mo)
	gatherer := ask.NewGatherer(db, gatherOpts(t))

	var unique []Question
	for _, q := range loadQuestions(t) {
		if q.Resolution == ResolutionUnique {
			unique = append(unique, q)
		}
	}
	t.Logf("unique questions=%d margin=%.2f", len(unique), margin)

	type groundingRow struct {
		q     Question
		asked bool
		found bool
	}
	var rows []groundingRow
	var grounded, groundedOfNotAsked, notAsked int
	for _, q := range unique {
		hits, named := hitsFor(t, ctx, r, expansions, expansionRepos, q)
		d, err := router.Route(ctx, q.Text, ask.AudienceDev, ask.LanguageEN, hits, named, false)
		if err != nil {
			t.Fatalf("route %q: %v", q.Text, err)
		}
		// Gathering always starts from ALL hits, matching pipeline.go: routing
		// decides whether to ask, not what to read. A "unique" question is
		// never expected to make the router ask, but if it does, that turn
		// never reaches gathering in production — sources is empty here in
		// the same way, so the question counts as ungrounded in the all-up
		// number rather than hiding behind a gather call the real pipeline
		// would never run.
		var sources []ask.Source
		if !d.Ask {
			sources, err = gatherer.Gather(ctx, hits)
			if err != nil {
				t.Fatalf("gather %q: %v", q.Text, err)
			}
		}
		found := false
		if len(q.Candidates) > 0 {
			found = hopOfCandidate(sources, q.Candidates[0]) >= 0
		}
		if found {
			grounded++
		}
		if !d.Ask {
			notAsked++
			if found {
				groundedOfNotAsked++
			}
		}
		rows = append(rows, groundingRow{q: q, asked: d.Ask, found: found})
	}

	t.Logf("")
	t.Logf("grounded, all %d unique questions       = %.3f (%d/%d) — includes any the router asked about", len(unique), float64(grounded)/float64(len(unique)), grounded, len(unique))
	t.Logf("grounded, of the %d NOT asked about      = %.3f (%d/%d) — the number comparable to the published 0.955, which had no routing step in front of it",
		notAsked, safeDiv(groundedOfNotAsked, notAsked), groundedOfNotAsked, notAsked)
	if notAsked < len(unique) {
		t.Logf("NOTE: the router asked about %d of %d unique questions — that is itself a routing miscall on a cohort defined to have exactly one answer",
			len(unique)-notAsked, len(unique))
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].found != rows[j].found {
			return !rows[i].found
		}
		return rows[i].q.Text < rows[j].q.Text
	})
	t.Logf("%-6s %-6s %s", "asked", "found", "question")
	for _, row := range rows {
		t.Logf("%-6v %-6v %s", row.asked, row.found, short(row.q.Text))
	}
}

// safeDiv reports a/b as a float, or 0 when b is 0 — printing a rate over an
// empty "not asked" cohort must not divide by zero if the router somehow asks
// about every unique question.
func safeDiv(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// TestResolutionExpectsAskMatchesTheSpec runs WITHOUT an endpoint: the
// criterion the routing arms score against is fixed here, in one place, the
// same way TestQuestionSetIsWellFormed fixes the shape of the question set.
func TestResolutionExpectsAskMatchesTheSpec(t *testing.T) {
	cases := []struct {
		res  Resolution
		want bool
	}{
		{ResolutionUnique, false},
		{ResolutionAmbiguous, true},
		{ResolutionComposition, false},
		// The reader named both sides. Asking is the one wrong answer.
		{ResolutionComparison, false},
	}
	for _, c := range cases {
		if got := resolutionExpectsAsk(c.res); got != c.want {
			t.Errorf("resolutionExpectsAsk(%s) = %v, want %v", c.res, got, c.want)
		}
	}
}

// TestSweepBookkeepingMatchesTheLadder runs WITHOUT an endpoint: it is the
// guard that the sweep's bookkeeping — deciding when Related and Judge have
// to be paid for, and re-deciding at each margin from the cached
// Rank/Related/Judge triple — stays in step with ask.Dominates and
// ask.Decide as margins or candidate scores change.
func TestSweepBookkeepingMatchesTheLadder(t *testing.T) {
	dominant := []ask.Candidate{{Score: 0.60}, {Score: 0.20}} // ratio 0.667, clears every margin in the sweep
	tight := []ask.Candidate{{Score: 0.51}, {Score: 0.49}}    // ratio 0.039

	// A margin looser than the ratio dominates: the ladder never needs to go
	// on, so Related/Judge are never asked for, and the decision is Ask=false
	// regardless of what they would have said.
	if anyMarginNeedsLadder(dominant, []float64{0.10, 0.40}) {
		t.Error("a dominant pair never needs the ladder to go on at any margin in this sweep")
	}
	if ask.Decide(dominant, 0.10, true /* must not be read */, true /* must not be read */, 0, false, true, projects.Map{}) {
		t.Error("a dominant pair must answer without asking regardless of related/judged")
	}

	// A margin tighter than the ratio does not dominate: the ladder goes on.
	if !anyMarginNeedsLadder(tight, []float64{0.10}) {
		t.Error("a close pair below the margin needs the ladder to go on")
	}

	// Once past Dominates, a manifest dependency short-circuits the judge.
	if got := ask.Decide(tight, 0.10, true, true /* must not be read */, 0, false, true, projects.Map{}); got {
		t.Error("a manifest dependency must not ask even if the judge would have said ask")
	}

	// Once past Dominates and with no manifest dependency, the judge's answer
	// is what decides — never defaulted.
	if got := ask.Decide(tight, 0.10, false, true, 0, false, true, projects.Map{}); !got {
		t.Error("ask.Decide must read the judge's answer once the margin does not dominate and nothing is related")
	}
	if got := ask.Decide(tight, 0.10, false, false, 0, false, true, projects.Map{}); got {
		t.Error("ask.Decide must read the judge's answer, not default to true")
	}

	// A sweep where ONE margin (not the first, not the last) needs the ladder
	// still reports anyMarginNeedsLadder — the whole point of checking every
	// margin rather than just the extremes.
	mixed := []ask.Candidate{{Score: 0.55}, {Score: 0.50}} // ratio 0.0909
	if !anyMarginNeedsLadder(mixed, []float64{0.05, 0.0909 + 0.001, 0.20}) {
		t.Error("a margin strictly above the ratio still needs the ladder to go on for that margin")
	}
}
