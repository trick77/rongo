package eval

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/retrieve"
)

// repoRungTurn is one question with every paid rung already run, so the
// settings below can be replayed over it without calling anything again.
type repoRungTurn struct {
	q     Question
	want  bool
	all   []ask.Candidate
	named int
	// relatedSpanning is the manifest answer Route would get with the rung
	// live, relatedCapped the one it would get without. Which of the two
	// applies is a function of the setting, so both are paid for up front.
	relatedSpanning bool
	relatedCapped   bool
	judged          bool
	judgeRan        bool
	// judgedRepos is the judge's answer on the REPOSITORY-grained list, which
	// is what a defer would actually pass it. The two are different questions
	// and the first sweep only asked the first one: the judge's prompt prints
	// "Repository X, module Y" per candidate, so on the capped module list the
	// top rows can name the same repository twice and the cross-repository
	// ambiguity never reaches the model at all.
	judgedRepos   bool
	judgeReposRan bool
	ratio         float64 // second repository's best, as a share of the leader
	spansNow      bool
}

// TestEvalMeasureRepoRungSweep asks what the repository rung should do, over
// the whole ladder rather than over the rung in isolation.
//
// 2026-09-06 attributed 16 of the ladder's 18 wrong decisions to that one
// rung: it fires on repository SPAN, with no test of whether the second
// repository's candidate is worth offering. TestEvalMeasureRepoRungShape then
// dumped the 46 spanning turns and found that no scalar separates them — the
// correct cards sit at second-repository score shares of 0.42 to 0.68, right
// among the wrong ones. So this arm measures two different answers in one run:
//
//   - a BAR: the rung fires only when the second repository's best candidate
//     is within some share of the leader. The shape dump says the useful
//     settings are the low ones; the high ones win on paper by switching the
//     rung off for most of its wins and betting the judge picks them back up.
//   - DEFER: the rung does not fire at all, and a spanning turn falls through
//     to the margin and then to the judge — which owned 0 to 1 of the wrong
//     decisions where this rung owns 16.
//
// Both share the same paid calls, which is why they are one arm and not two.
// Rank, Related and Judge run AT MOST ONCE per question and the settings are
// replayed over the result with ask.DecideWhySpans, the same way the margin
// sweep replays ask.Decide.
//
// Related is paid TWICE where it matters, and that is not waste: Route asks it
// over the repository-grained list when the rung is live and over the capped
// module list when it is not, so a setting that flips the rung flips which
// question was asked. Measuring one and replaying it under both would report a
// manifest edge the product would never have seen.
func TestEvalMeasureRepoRungSweep(t *testing.T) {
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
	expansionRepos := loadExpansionRepos(t)
	margin := routeMargin(t)
	router := ask.NewRouter(client, db, margin, moduleOpts(t))

	var turns []repoRungTurn
	judgeCalls := 0
	for _, q := range loadQuestions(t) {
		hits, named := hitsFor(t, ctx, r, expansions, expansionRepos, q)
		ranked, err := router.Rank(ctx, hits)
		if err != nil {
			t.Fatalf("rank %q: %v", q.Text, err)
		}
		tn := repoRungTurn{q: q, want: resolutionExpectsAsk(q.Resolution), all: ranked.All, named: len(named)}
		tn.spansNow = ask.SpansRepos(ranked.All, len(named))
		tn.ratio = secondRepoShare(ranked.All)

		// A question that named its repository is settled above every rung
		// under test, and the paid rungs are skipped exactly as Route skips
		// them.
		if len(named) == 0 && len(ranked.All) > 0 {
			tn.relatedSpanning, err = router.Related(ctx, ask.RepoCandidates(ranked.All))
			if err != nil {
				t.Fatalf("related (spanning) %q: %v", q.Text, err)
			}
			tn.relatedCapped, err = router.Related(ctx, ranked.Capped)
			if err != nil {
				t.Fatalf("related (capped) %q: %v", q.Text, err)
			}
			// The judge is paid whenever ANY setting could reach it: a turn
			// that spans today reaches it under defer, and one that does not
			// reaches it whenever the leader is not dominant. Paying per
			// setting instead would re-roll the same question six times and
			// mix the judge's own spread into the comparison — 2026-09-06
			// measured that spread at one question per run even pinned.
			if tn.spansNow || !ask.Dominates(ranked.All, margin) {
				tn.judged, err = router.Judge(ctx, q.Text, ranked.Capped)
				if err != nil {
					t.Fatalf("judge %q: %v", q.Text, err)
				}
				tn.judgeRan = true
				judgeCalls++
			}
			// The same question asked the way a defer would ask it: one
			// candidate per repository. Only spanning turns have a second
			// repository to show, so only they are worth the call.
			if tn.spansNow {
				tn.judgedRepos, err = router.Judge(ctx, q.Text, ask.RepoCandidates(ranked.All))
				if err != nil {
					t.Fatalf("judge over repositories %q: %v", q.Text, err)
				}
				tn.judgeReposRan = true
				judgeCalls++
			}
		}
		turns = append(turns, tn)
	}

	wantAsk := 0
	for _, tn := range turns {
		if tn.want {
			wantAsk++
		}
	}

	// judgedBy selects which of the two judgements a setting reads. Every bar
	// setting reads the module-grained one, because a turn that fails the bar
	// reaches the judge exactly as Route reaches it today. Only a defer gets to
	// ask the other question, and only on a turn that has a second repository
	// to show.
	onCapped := func(tn repoRungTurn) bool { return tn.judged }
	onRepos := func(tn repoRungTurn) bool {
		if tn.judgeReposRan {
			return tn.judgedRepos
		}
		return tn.judged
	}

	report := func(label string, spans func(repoRungTurn) bool, judgedBy func(repoRungTurn) bool) {
		var correct, ambigOK, cards, missed int
		byRung := map[string]int{}
		for _, tn := range turns {
			live := spans(tn)
			related := tn.relatedCapped
			judged := judgedBy(tn)
			switch {
			case live:
				related = tn.relatedSpanning
			case ask.Dominates(tn.all, margin):
				// Route short-circuits a dominant, non-spanning turn before it
				// pays for the manifest or the judge, and passes false for
				// both (route.go). Feeding the values this arm happens to have
				// would not change the decision — the margin settles it either
				// way — but it would file a wrong no-ask under repo_deps
				// instead of margin, and the rung breakdown is the whole point
				// of this table.
				related, judged = false, false
			}
			got, rung := ask.DecideWhySpans(tn.all, margin, related, judged, tn.named, false, true, live)
			if got == tn.want {
				correct++
				if tn.want {
					ambigOK++
				}
				continue
			}
			byRung[rung]++
			// The two ways to be wrong, priced apart below: a card nobody
			// needed, and an answer composed across genuine alternatives.
			if tn.want {
				missed++
			} else {
				cards++
			}
		}
		// too_broad stays in the breakdown even though it is unreachable on a
		// three-repository corpus: it is reachable the moment a fourth is
		// indexed, and a rung missing from this line would take its wrong
		// decisions with it while they still counted in cards, so the columns
		// would quietly stop adding up.
		t.Logf("%-16s %-14s %-14s repository=%d repo_deps=%d judge=%d margin=%d too_broad=%d",
			label,
			fraction(correct, len(turns)),
			fraction(ambigOK, wantAsk),
			byRung["repository"], byRung["repo_deps"], byRung["judge"], byRung["margin"], byRung["too_broad"])
		// The same pricing the routing arm prints, from the same helper: a
		// second copy of the crossover formula is how the two arms would come
		// to disagree about what a setting costs.
		reportRoutingCost(t, cards, missed, wantAsk)
	}

	t.Logf("questions=%d margin=%.2f judge calls=%d", len(turns), margin, judgeCalls)
	t.Logf("never ask = %s", fraction(len(turns)-wantAsk, len(turns)))
	t.Logf("")
	t.Logf("%-16s %-14s %-14s %s", "setting", "overall", "ambiguous", "wrong by rung")
	report("baseline", func(tn repoRungTurn) bool { return tn.spansNow }, onCapped)
	for _, bar := range []float64{0.50, 0.60, 0.70, 0.80, 0.85} {
		b := bar
		report(fmt.Sprintf("bar >= %.2f", b), func(tn repoRungTurn) bool { return tn.spansNow && tn.ratio >= b }, onCapped)
	}
	report("defer, modules", func(repoRungTurn) bool { return false }, onCapped)
	report("defer, repos", func(repoRungTurn) bool { return false }, onRepos)
}

// secondRepoShare is the best candidate from the SECOND repository as a share
// of the leader's score, or 0 when every candidate comes from one repository.
// Relative for the same reason the margin and worthOffering's floor are: fused
// scores have no fixed range.
func secondRepoShare(cs []ask.Candidate) float64 {
	if len(cs) == 0 || cs[0].Score <= 0 {
		return 0
	}
	for _, c := range cs {
		if c.Repo != cs[0].Repo {
			return c.Score / cs[0].Score
		}
	}
	return 0
}

func fraction(n, total int) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f (%d/%d)", float64(n)/float64(total), n, total)
}
