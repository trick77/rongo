package eval

import (
	"context"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/retrieve"
)

// routeSearchK mirrors ask.searchK (unexported), the same way gatherSearchK in
// gather_test.go does: the running pipeline searches 20 hits before routing.
const routeSearchK = 20

// routeMargins is the sweep the phase 4b spec asks for. BACKEND_ROUTE_MARGIN's
// chosen value comes OUT of the accuracy table this produces; the constant is
// not assumed by it.
var routeMargins = []float64{0.10, 0.15, 0.20, 0.25, 0.30, 0.40}

// resolutionExpectsAsk is the criterion fixed before any run: an ambiguous
// question is served correctly only by a clarification, and a unique or
// composition question is served correctly by an answer — never a question.
func resolutionExpectsAsk(res Resolution) bool {
	return res == ResolutionAmbiguous
}

// askAt reproduces Router.Route's ask/answer decision for ONE margin, from
// data computed once per question: Rank's output (the grouped candidates and
// the manifest-dependency check, neither of which depends on margin) and, if
// the ladder needs it at all, the judge's single answer.
//
// This is what lets the margin sweep below avoid paying for the judge call at
// every one of six margins: askAt is called six times, r.Judge at most once.
func askAt(ranked ask.Ranked, margin float64, judged bool) bool {
	if ask.Dominates(ranked.All, margin) {
		return false
	}
	if ranked.Related {
		return false
	}
	return judged
}

// needsJudge reports whether ANY of margins would reach the judge rung for
// this ranking — i.e. whether askAt would ever read judged for one of them.
// Checking every margin, not just the loosest or the strictest, is what makes
// this correct regardless of how the sweep's margins are chosen.
func needsJudge(ranked ask.Ranked, margins []float64) bool {
	if ranked.Related {
		return false
	}
	for _, m := range margins {
		if !ask.Dominates(ranked.All, m) {
			return true
		}
	}
	return false
}

// rankAndJudge runs Rank once and, only if some margin in margins would reach
// the judge rung, Judge once. Every accuracy figure for this question at any
// of margins is then derived from the two return values via askAt, with no
// further model call — the bookkeeping the margin sweep and the ShortGate/Pro
// comparison both build on.
func rankAndJudge(ctx context.Context, t *testing.T, r *ask.Router, question string, hits []retrieve.Hit, margins []float64) (ask.Ranked, bool) {
	t.Helper()
	ranked, err := r.Rank(ctx, hits)
	if err != nil {
		t.Fatalf("rank %q: %v", question, err)
	}
	if !needsJudge(ranked, margins) {
		return ranked, false
	}
	judged, err := r.Judge(ctx, question, ranked.Capped)
	if err != nil {
		t.Fatalf("judge %q: %v", question, err)
	}
	return ranked, judged
}

// llmClientForRouting builds the model client the routing arms share. Skips —
// never fails — when no endpoint is configured, exactly like
// TestExpandQuestions: a routing arm without BACKEND_LLM_BASE_URL cannot call
// the judge at all.
func llmClientForRouting(t *testing.T) *llm.Client {
	t.Helper()
	base := os.Getenv("BACKEND_LLM_BASE_URL")
	if base == "" {
		t.Skip("BACKEND_LLM_BASE_URL is unset")
	}
	return llm.NewClient(llm.Config{
		BaseURL: base,
		APIKey:  os.Getenv("BACKEND_LLM_API_KEY"),
		Timeout: 2 * time.Minute,
	}, nil)
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

// hitsFor runs the frozen expansion for q through Search. Reusing
// expansions.json is the same reuse gather_test.go relies on: a routing
// measurement should not also pay for a fresh understanding call per
// question, and freezing is what makes the measurement repeatable.
func hitsFor(t *testing.T, ctx context.Context, r *retrieve.Retriever, expansions map[string][]string, q Question) []retrieve.Hit {
	t.Helper()
	texts, ok := expansions[q.Text]
	if !ok {
		t.Fatalf("no expansion recorded for %q — run TestExpandQuestions first", q.Text)
	}
	hits, err := r.Search(ctx, retrieve.Query{Texts: texts, K: routeSearchK})
	if err != nil {
		t.Fatalf("search %q: %v", q.Text, err)
	}
	return hits
}

// routingRow is one question's outcome under one routing arm, kept for the
// per-question detail printed after the aggregate.
type routingRow struct {
	q    Question
	want bool
	got  bool
}

func (r routingRow) correct() bool { return r.got == r.want }

// reportRouting prints one arm's accuracy, overall and split by resolution —
// the split matters because the two ways to be WRONG are opposite mistakes:
// asking about a unique/composition question annoys the reader, answering an
// ambiguous one silently guesses.
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
		t.Logf("  unique+composition (want Ask=false)    = %.3f (%d/%d)",
			float64(otherCorrect)/float64(otherN), otherCorrect, otherN)
	}

	sorted := append([]routingRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		// Wrong first, then by question text, so a misroute never scrolls off
		// the bottom of a long run.
		if sorted[i].correct() != sorted[j].correct() {
			return !sorted[i].correct()
		}
		return sorted[i].q.Text < sorted[j].q.Text
	})
	t.Logf("%-5s %-5s %-5s %-12s %s", "want", "got", "ok", "resolution", "question")
	for _, row := range sorted {
		t.Logf("%-5v %-5v %-5v %-12s %s", row.want, row.got, row.correct(), row.q.Resolution, short(row.q.Text))
	}
}

// TestEvalMeasureRouting measures the routing decision against Pro, over all
// 61 questions, before non-Pro (ShortGate) is written into config.go
// permanently — the phase 4b spec makes this comparison mandatory: "die
// Trefferquote des Routens wird gegen Pro gemessen, bevor non-Pro dort
// festgeschrieben wird. Behaupten reicht hier nicht."
//
// Both arms share one Rank per question (no margin dependency, no model
// call) and call Judge only when the ladder would actually reach it — never
// Route()'s naming step, which nothing here reads.
func TestEvalMeasureRouting(t *testing.T) {
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
	r := retrieve.New(db, embedder)
	expansions := loadExpansions(t)
	questions := loadQuestions(t)
	margin := routeMargin(t)
	mo := moduleOpts(t)

	shortGate := ask.NewRouter(client, db, margin, mo)
	pro := ask.NewRouter(client, db, margin, mo).WithJudgeDeployment(nil)

	t.Logf("questions=%d margin=%.2f", len(questions), margin)

	var shortRows, proRows []routingRow
	for _, q := range questions {
		hits := hitsFor(t, ctx, r, expansions, q)
		want := resolutionExpectsAsk(q.Resolution)

		sRanked, sJudged := rankAndJudge(ctx, t, shortGate, q.Text, hits, []float64{margin})
		shortRows = append(shortRows, routingRow{q: q, want: want, got: askAt(sRanked, margin, sJudged)})

		pRanked, pJudged := rankAndJudge(ctx, t, pro, q.Text, hits, []float64{margin})
		proRows = append(proRows, routingRow{q: q, want: want, got: askAt(pRanked, margin, pJudged)})
	}

	t.Logf("")
	reportRouting(t, "judge on ShortGate (non-Pro)", shortRows)
	t.Logf("")
	reportRouting(t, "judge on Pro", proRows)
}

// TestEvalMeasureRoutingMarginSweep reports routing accuracy at every margin
// in routeMargins, over all 61 questions, on the ShortGate judge — the
// deployment BACKEND_ROUTE_MARGIN actually gates in production. The chosen
// constant comes out of this table; the table does not assume it.
//
// Rank and (at most) one Judge call run ONCE per question and are reused
// across every margin via askAt — the sweep does not re-pay for the judge six
// times over.
func TestEvalMeasureRoutingMarginSweep(t *testing.T) {
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
	r := retrieve.New(db, embedder)
	expansions := loadExpansions(t)
	questions := loadQuestions(t)
	mo := moduleOpts(t)

	// The margin passed to NewRouter here is never read: this arm calls Rank
	// and Judge directly, bypassing Route()'s own margin check so every
	// margin in the sweep can be tried against the SAME ranking and the SAME
	// judgement. 0 makes that plain rather than implying a margin matters.
	router := ask.NewRouter(client, db, 0, mo)

	t.Logf("questions=%d margins=%v", len(questions), routeMargins)

	type perQuestion struct {
		ranked ask.Ranked
		judged bool
		want   bool
		q      Question
	}
	rows := make([]perQuestion, 0, len(questions))
	for _, q := range questions {
		hits := hitsFor(t, ctx, r, expansions, q)
		ranked, judged := rankAndJudge(ctx, t, router, q.Text, hits, routeMargins)
		rows = append(rows, perQuestion{ranked: ranked, judged: judged, want: resolutionExpectsAsk(q.Resolution), q: q})
	}

	t.Logf("")
	t.Logf("%-8s %-10s %s", "margin", "accuracy", "correct/total")
	for _, margin := range routeMargins {
		correct := 0
		for _, row := range rows {
			if askAt(row.ranked, margin, row.judged) == row.want {
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
func TestEvalMeasureRoutingGrounding(t *testing.T) {
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
	r := retrieve.New(db, embedder)
	expansions := loadExpansions(t)
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
	var grounded int
	for _, q := range unique {
		hits := hitsFor(t, ctx, r, expansions, q)
		d, err := router.Route(ctx, q.Text, hits)
		if err != nil {
			t.Fatalf("route %q: %v", q.Text, err)
		}
		// Gathering always starts from ALL hits, matching pipeline.go: routing
		// decides whether to ask, not what to read. A "unique" question is
		// never expected to make the router ask, but if it does, that turn
		// never reaches gathering in production — sources is empty here in
		// the same way, so the question counts as ungrounded rather than
		// hiding behind a gather call the real pipeline would never run.
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
		rows = append(rows, groundingRow{q: q, asked: d.Ask, found: found})
	}

	t.Logf("")
	t.Logf("grounded = %.3f (%d/%d) — published anchor to beat: 0.955", float64(grounded)/float64(len(unique)), grounded, len(unique))
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
	}
	for _, c := range cases {
		if got := resolutionExpectsAsk(c.res); got != c.want {
			t.Errorf("resolutionExpectsAsk(%s) = %v, want %v", c.res, got, c.want)
		}
	}
}

// TestAskAtAndNeedsJudge runs WITHOUT an endpoint: it is the guard that the
// sweep's bookkeeping — deciding when the judge is needed and reproducing
// Route()'s ladder from a cached Rank/Judge pair — matches Router.Dominates
// and stays correct as margins or candidate scores change.
func TestAskAtAndNeedsJudge(t *testing.T) {
	dominant := ask.Ranked{All: []ask.Candidate{{Score: 0.60}, {Score: 0.20}}} // ratio 0.667, clears every margin in the sweep
	tight := ask.Ranked{All: []ask.Candidate{{Score: 0.51}, {Score: 0.49}}}    // ratio 0.039
	related := ask.Ranked{All: tight.All, Related: true}

	// A margin looser than the ratio dominates: no judge needed, Ask=false.
	if needsJudge(dominant, []float64{0.10, 0.40}) {
		t.Error("a dominant pair never needs the judge at any margin in this sweep")
	}
	if askAt(dominant, 0.10, true /* must not be read */) {
		t.Error("a dominant pair must answer without asking regardless of the judge")
	}

	// A margin tighter than the ratio does not dominate: the judge decides.
	if !needsJudge(tight, []float64{0.10}) {
		t.Error("a close pair below the margin needs the judge")
	}
	if got := askAt(tight, 0.10, true); !got {
		t.Error("askAt must read the judge's answer once the margin does not dominate")
	}
	if got := askAt(tight, 0.10, false); got {
		t.Error("askAt must read the judge's answer, not default to true")
	}

	// A manifest dependency short-circuits the judge even when close.
	if needsJudge(related, []float64{0.10}) {
		t.Error("a manifest dependency answers composition without the judge")
	}
	if askAt(related, 0.10, true /* must not be read */) {
		t.Error("a manifest dependency must not ask even if the judge would have said ask")
	}

	// A sweep where ONE margin (not the first, not the last) needs the judge
	// still reports needsJudge — the whole point of checking every margin
	// rather than just the extremes.
	mixed := ask.Ranked{All: []ask.Candidate{{Score: 0.55}, {Score: 0.50}}} // ratio 0.0909
	if !needsJudge(mixed, []float64{0.05, 0.0909 + 0.001, 0.20}) {
		t.Error("a margin strictly above the ratio still needs the judge for that margin")
	}
}
