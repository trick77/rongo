package retrieve

import "testing"

// repoHits builds one lane's worth of hits, all from the same repository.
func repoHits(repo string, ids ...int64) []Hit {
	out := make([]Hit, len(ids))
	for i, id := range ids {
		out[i] = Hit{ChunkID: id, Repo: repo, Path: "p"}
	}
	return out
}

func reposOf(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Repo
	}
	return out
}

func TestFuseWeightedDiverse_decayOfOneLeavesTheOrderAlone(t *testing.T) {
	// Given: two repositories, interleaved by the lanes.
	lanes := []Lane{
		{Name: "semantic:0", Hits: append(repoHits("a", 1, 2, 3), repoHits("b", 4, 5)...), Weight: WeightSemantic},
		{Name: "keyword:strict", Hits: repoHits("a", 3, 1), Weight: WeightKeywordStrict},
	}

	// When: the decay is switched off.
	plain := FuseWeighted(lanes, 5)
	got := FuseWeightedDiverse(lanes, 5, 1.0)

	// Then: byte for byte the undiversified ranking. The knob's off position
	// has to be the behaviour that shipped, or every measurement against it
	// compares two changes at once.
	if len(got) != len(plain) {
		t.Fatalf("length %d, want %d", len(got), len(plain))
	}
	for i := range got {
		if got[i].ChunkID != plain[i].ChunkID {
			t.Fatalf("rank %d is chunk %d, want %d — decay 1.0 must not reorder: %v vs %v",
				i, got[i].ChunkID, plain[i].ChunkID, ids(got), ids(plain))
		}
	}
}

func TestFuseWeightedDiverse_liftsASecondRepositoryIntoTheCut(t *testing.T) {
	// Given: one repository owns the whole top of the list and the second
	// implementation sits just below the cut. This is the measured failure:
	// only 7 of 16 ambiguous questions retrieved both of their answers, so the
	// router was asked to arbitrate between candidates it could not see.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: append(repoHits("a", 1, 2, 3, 4), repoHits("b", 9)...), Weight: WeightKeywordStrict},
	}

	// When: three hits are wanted and the decay is on.
	got := FuseWeightedDiverse(lanes, 3, 0.5)

	// Then: repository b is represented, and the strongest hit still leads.
	if got[0].ChunkID != 1 {
		t.Errorf("rank 0 is chunk %d, want 1 — diversity must not demote the best hit", got[0].ChunkID)
	}
	if rankOf(got, 9) >= len(got) {
		t.Errorf("repository b is absent from %v (repos %v) — the second implementation never reaches the router",
			ids(got), reposOf(got))
	}
}

func TestFuseWeightedDiverse_diversifiesBeforeTruncating(t *testing.T) {
	// Given: the second repository's only hit is at rank 5 of the fused list,
	// below a cut of 3. Diversifying the already-truncated list could not
	// possibly find it — the material has to be there when the decay is
	// applied.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: append(repoHits("a", 1, 2, 3, 4, 5), repoHits("b", 9)...), Weight: WeightKeywordStrict},
	}

	// When
	got := FuseWeightedDiverse(lanes, 3, 0.4)

	// Then
	if rankOf(got, 9) >= len(got) {
		t.Errorf("chunk 9 missing from %v — it was cut away before the decay could lift it", ids(got))
	}
}

func TestFuseWeightedDiverse_keepsEveryHitAndStaysDeterministic(t *testing.T) {
	// Given: a harsh decay, which all but zeroes every repeat from a
	// repository.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: append(repoHits("a", 1, 2, 3), repoHits("b", 7, 8)...), Weight: WeightKeywordStrict},
	}

	// When: run twice, asking for more than there is.
	first := FuseWeightedDiverse(lanes, 99, 0.05)
	second := FuseWeightedDiverse(lanes, 99, 0.05)

	// Then: nothing is dropped — reordering is not filtering — and two runs
	// agree. A search that returns a different list for the same corpus reads
	// to the user as "ask twice, get two answers".
	if len(first) != 5 {
		t.Fatalf("got %d hits, want all 5: %v", len(first), ids(first))
	}
	for i := range first {
		if first[i].ChunkID != second[i].ChunkID {
			t.Fatalf("two runs disagree at rank %d: %v vs %v", i, ids(first), ids(second))
		}
	}
}

func TestFuseWeightedDiverse_outOfRangeDecayIsOff(t *testing.T) {
	// Given: settings nobody should pass. Zero is the one that matters: it is
	// the zero value of the field, so a Retriever assembled as a struct literal
	// must rank exactly as it shipped rather than at the harshest diversity
	// there is. A negative multiplier would be worse still — it sorts a
	// repository's later hits ABOVE its best one.
	lanes := []Lane{
		{Name: "semantic:0", Hits: append(repoHits("a", 1, 2, 3), repoHits("b", 4)...), Weight: WeightSemantic},
		{Name: "keyword:strict", Hits: repoHits("a", 2), Weight: WeightKeywordStrict},
	}
	plain := FuseWeighted(lanes, 4)

	for _, decay := range []float64{0, -1, 2} {
		// When
		got := FuseWeightedDiverse(lanes, 4, decay)

		// Then
		for i := range plain {
			if got[i].ChunkID != plain[i].ChunkID {
				t.Errorf("decay %v reordered rank %d: %v, want %v", decay, i, ids(got), ids(plain))
				break
			}
		}
	}
}
