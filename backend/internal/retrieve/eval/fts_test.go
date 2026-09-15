// The identifier arm: does giving the keyword lane a second column — the
// repo/path header, the symbol breadcrumb and every identifier of the chunk
// split into words — find code the lane could not reach, and does it cost
// anything where the lane already worked?
//
//	hack/run-eval.sh 'TestEvalMeasureFTS$'
//
// Deterministic by construction: no reranker, no model call, one database. The
// arms differ in a bm25 column weight and a lane weight, nothing else.
package eval

import (
	"context"
	"sort"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/retrieve"
)

// codeTextOf picks the guessed CODE VOCABULARY out of a frozen expansion.
//
// Understanding.SearchTexts emits the raw question, the business-language
// restatement and the code terms, in that order and dropping a blank one — so
// three texts means the last is the code terms and anything shorter means the
// step guessed none that the frozen file can tell apart from the restatement.
// Read positionally because the frozen file records Texts alone; a question
// with two texts contributes no code rung rather than a guessed one.
func codeTextOf(texts []string) string {
	if len(texts) != 3 {
		return ""
	}
	return texts[2]
}

// ftsArm is one configuration of the keyword lane.
type ftsArm struct {
	name string
	r    *retrieve.Retriever
}

// TestEvalMeasureFTS reports, per arm, recall@5, recall@20, MRR and how much
// of the material an answer is written from the gatherer actually reaches —
// plus the two cohorts that stop a recall win from hiding a loss, and the
// doc-led axis, because aux carries a path and a README's path is all header.
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

	// The aux column is written by the indexer, not derivable in SQL: 0024
	// backfills the source column and empties last_sha so the next full run
	// fills aux. An arm run before that run would measure an empty column and
	// report the baseline twice.
	var withAux int
	if err := db.QueryRow(`SELECT count(*) FROM chunks_fts WHERE aux <> ''`).Scan(&withAux); err != nil {
		t.Fatalf("count the aux column: %v", err)
	}
	if withAux == 0 {
		t.Fatal("no chunk has an aux column yet: run TestEvalIndex first")
	}
	t.Logf("chunks with an aux column: %d", withAux)

	expansions := loadExpansions(t)
	expansionRepos := loadExpansionRepos(t)
	questions := loadQuestions(t)
	g := ask.NewGatherer(db, gatherOpts(t))

	off := retrieve.New(db, evalEmbedder(t))
	off.AuxWeight = 0
	on := retrieve.New(db, evalEmbedder(t))
	on.AuxWeight = retrieve.DefaultAuxWeight
	arms := []ftsArm{
		{"aux off (today's lane)", off},
		{"aux 0.5", on},
	}
	// The code rung is a second, independent change: it reweighs the OR floor
	// of the code-terms text. Measured only when asked for, so the column's own
	// number is never read off a run that moved two things.
	codeLane := envOr("BACKEND_EVAL_CODE_LANE", "0") == "1"
	if codeLane {
		both := retrieve.New(db, evalEmbedder(t))
		both.AuxWeight = retrieve.DefaultAuxWeight
		both.CodeWeight = retrieve.WeightKeywordCode
		arms = append(arms, ftsArm{"aux 0.5 + code rung 0.8", both})
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
				Code:     codeTextOf(texts),
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

// TestAuxWeightFromEnvIsAHarnessSwitch runs WITHOUT an endpoint. "0" has to
// mean the column off and not "unset, use the shipped value", or the baseline
// arm silently measures the arm — two identical tables and nothing in the
// output saying why.
func TestAuxWeightFromEnvIsAHarnessSwitch(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  float64
	}{
		{"", retrieve.DefaultAuxWeight},
		{"0", 0},
		{"0.5", 0.5},
		{"1", 1},
	} {
		t.Setenv("BACKEND_EVAL_FTS_AUX", tc.value)
		if got := auxWeightFromEnv(t); got != tc.want {
			t.Errorf("auxWeightFromEnv() with BACKEND_EVAL_FTS_AUX=%q = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestCodeTextOfReadsTheThirdExpansion runs WITHOUT an endpoint. The frozen
// file records Texts alone, so the code rung's input is positional — and a
// question the step guessed no identifiers for must contribute no rung rather
// than the business-language restatement under a code weight.
func TestCodeTextOfReadsTheThirdExpansion(t *testing.T) {
	if got := codeTextOf([]string{"question", "restatement", "PromoMailJob dispatchRetry"}); got != "PromoMailJob dispatchRetry" {
		t.Errorf("codeTextOf(...) = %q, want the code terms", got)
	}
	for _, texts := range [][]string{nil, {"question"}, {"question", "restatement"}} {
		if got := codeTextOf(texts); got != "" {
			t.Errorf("codeTextOf(%v) = %q, want no code rung", texts, got)
		}
	}
}
