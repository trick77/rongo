package retrieve

import "testing"

func TestFuseWeightedDecayed_dropsADocOutOfTheCut(t *testing.T) {
	// Given: two documents rank above the code that implements what they
	// describe — the ordinary shape of a natural-language question, which
	// matches prose before it matches identifiers.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: pathHits("rongo",
			"AGENTS.md",
			"README.md",
			"internal/llm/client.go",
		), Weight: WeightKeywordStrict},
	}

	// When: two hits are wanted and the doc decay is on.
	got := FuseWeightedDecayed(lanes, 2, Decays{Repo: DefaultRepoDecay, Doc: 0.35})

	// Then: the code that ranked below both documents is in the cut.
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2", len(got))
	}
	var code int
	for _, h := range got {
		if !IsDocPath(h.Path) {
			code++
		}
	}
	if code != 1 {
		t.Fatalf("got %d code hits in the cut, want 1: %+v", code, got)
	}
}

func TestFuseWeightedDecayed_docDemotionShowsInTheScore(t *testing.T) {
	// Given: one doc hit and one code hit at the same lane rank, so fusion
	// alone would score them identically.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: []Hit{{ChunkID: 1, Repo: "rongo", Path: "internal/llm/client.go"}}, Weight: WeightKeywordStrict},
		{Name: "semantic", Hits: []Hit{{ChunkID: 2, Repo: "rongo", Path: "docs/models.md"}}, Weight: WeightKeywordStrict},
	}

	// When: fused with the doc decay at a half.
	got := FuseWeightedDecayed(lanes, 2, Decays{Repo: DefaultRepoDecay, Doc: 0.5})

	// Then: the score written onto the hit carries the demotion, because the
	// routing floor in internal/ask reads it.
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2", len(got))
	}
	if IsDocPath(got[0].Path) {
		t.Fatalf("the document ranked first: %+v", got)
	}
	if got[1].Score >= got[0].Score {
		t.Fatalf("doc score %v not below code score %v", got[1].Score, got[0].Score)
	}
}

func TestFuseWeightedDecayed_testAndDocTakeTheHarsherFactorOnce(t *testing.T) {
	// Given: a path that is both a document and test material.
	const both = "docs/testdata/notes.md"
	if !IsDocPath(both) || !IsTestPath(both) {
		t.Fatalf("%q must be both a doc and a test for this case to mean anything", both)
	}
	lanes := []Lane{
		{Name: "keyword:strict", Hits: pathHits("rongo", both), Weight: WeightKeywordStrict},
	}

	// When: both decays are on, at different strengths.
	got := FuseWeightedDecayed(lanes, 1, Decays{Repo: DefaultRepoDecay, Test: 0.5, Doc: 0.25})
	plain := FuseWeightedDecayed(lanes, 1, Decays{Repo: DefaultRepoDecay})

	// Then: the harsher one is applied once, never the product of the two.
	if len(got) != 1 || len(plain) != 1 {
		t.Fatalf("got %d and %d hits, want 1 each", len(got), len(plain))
	}
	want := plain[0].Score * 0.25
	if got[0].Score != want {
		t.Fatalf("score = %v, want %v (the harsher factor once, not %v)", got[0].Score, want, plain[0].Score*0.5*0.25)
	}
}

func TestFuseWeightedDecayed_docDecayOfOneLeavesTheScoresAlone(t *testing.T) {
	// Given: a document and code in one lane.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: pathHits("rongo", "README.md", "internal/llm/client.go"), Weight: WeightKeywordStrict},
	}

	// When: the doc decay is off, which is what ships until the harness names
	// a value.
	got := FuseWeightedDecayed(lanes, 5, Decays{Repo: DefaultRepoDecay, Doc: 1.0})
	plain := FuseWeightedDecayed(lanes, 5, Decays{Repo: DefaultRepoDecay})

	// Then: the order and the scores are the ones fusion produced.
	if len(got) != len(plain) {
		t.Fatalf("got %d hits, want %d", len(got), len(plain))
	}
	for i := range got {
		if got[i].ChunkID != plain[i].ChunkID || got[i].Score != plain[i].Score {
			t.Fatalf("hit %d = %+v, want %+v", i, got[i], plain[i])
		}
	}
}
