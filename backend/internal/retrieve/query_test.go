package retrieve

import (
	"strings"
	"testing"
)

func matches(tiers []FTSTier) []string {
	out := make([]string, len(tiers))
	for i, t := range tiers {
		out[i] = t.Match
	}
	return out
}

func TestBuildFTSMatch_reEmitsRatherThanEscapes(t *testing.T) {
	// Given: a query carrying FTS5 syntax. MATCH is an expression language, so
	// anything passed through unaltered is a failed search, not a bad result.
	// When
	got := BuildFTSMatch(`AbandonedCartJob OR "drop table" NEAR/3 *`)

	// Then: every token in the output was built here, and each word is quoted.
	if strings.Contains(got, "*") || strings.Contains(got, "/") {
		t.Errorf("BuildFTSMatch() = %q, want no syntax characters to survive", got)
	}
	if !strings.Contains(got, `"abandonedcartjob"`) {
		t.Errorf("BuildFTSMatch() = %q, want the identifier lowercased and quoted", got)
	}
}

func TestBuildFTSQueries_aNaturalQuestionStillGetsAContentRung(t *testing.T) {
	// Given: a question phrased as a sentence. Without a stopword list the
	// content rung would be identical to the strict rung, get dropped as
	// redundant, and the ladder would collapse to its OR floor at weight 0.4 —
	// BELOW the semantic lane. Nothing would look broken: results still come
	// back, just semantic-dominated.
	q := "How is it prevented that two migrations run at the same time?"

	// When
	tiers := BuildFTSQueries(q)

	// Then
	if len(tiers) < 2 {
		t.Fatalf("BuildFTSQueries() = %v, want a content rung below the strict one", matches(tiers))
	}
	strict, content := tiers[0], tiers[1]
	if content.Weight != WeightKeywordContent {
		t.Errorf("second rung weight = %v, want the content weight %v", content.Weight, WeightKeywordContent)
	}
	if len(strings.Fields(content.Match)) >= len(strings.Fields(strict.Match)) {
		t.Errorf("content rung %q is not shorter than the strict rung %q; the function words were not dropped",
			content.Match, strict.Match)
	}
	for _, w := range []string{`"how"`, `"is"`, `"that"`} {
		if strings.Contains(content.Match, w) {
			t.Errorf("content rung still carries %s", w)
		}
	}
	if !strings.Contains(content.Match, `"migrations"`) {
		t.Errorf("content rung %q dropped the topical word", content.Match)
	}
}

func TestBuildFTSQueries_rungsDescendInWeight(t *testing.T) {
	// Given / When
	tiers := BuildFTSQueries("How is the teaser mail for abandoned carts sent?")

	// Then: the ladder must never hand a looser rung more confidence than a
	// stricter one, since each rung that answers becomes its own lane.
	for i := 1; i < len(tiers); i++ {
		if tiers[i].Weight > tiers[i-1].Weight {
			t.Errorf("rung %d (%v) outweighs rung %d (%v)", i, tiers[i].Weight, i-1, tiers[i-1].Weight)
		}
	}
	last := tiers[len(tiers)-1]
	if last.Weight >= WeightSemantic {
		t.Errorf("the recall floor weighs %v, which is not below the semantic lane's %v — 'shares one word' would outrank 'the embedding placed this near the question'",
			last.Weight, WeightSemantic)
	}
}

func TestBuildFTSQueries_dropsRedundantRungs(t *testing.T) {
	// Given: a single content word. Every relaxation reproduces the strict rung
	// exactly, and a duplicate rung would fuse the same rows in twice,
	// double-counting them against the other lanes.
	// When
	tiers := BuildFTSQueries("AbandonedCartJob")

	// Then
	seen := map[string]bool{}
	for _, tier := range tiers {
		if seen[tier.Match] {
			t.Errorf("rung %q appears twice: %v", tier.Match, matches(tiers))
		}
		seen[tier.Match] = true
	}
}

func TestBuildFTSQueries_allStopwordsLeavesTheStrictRungAlone(t *testing.T) {
	// Given: a question with no topical word at all.
	// When
	tiers := BuildFTSQueries("how is it that they were")

	// Then: there is no content query to fall back to, and inventing one from
	// nothing would match the whole corpus.
	if len(tiers) != 1 || tiers[0].Weight != WeightKeywordStrict {
		t.Errorf("BuildFTSQueries() = %v, want only the strict rung", matches(tiers))
	}
}

func TestBuildFTSQueries_unusableInputYieldsNoLane(t *testing.T) {
	// Given / When
	tiers := BuildFTSQueries("???  ...")

	// Then: no keyword lane, rather than an expression that matches everything.
	if tiers != nil {
		t.Errorf("BuildFTSQueries() = %v, want nil", matches(tiers))
	}
}
