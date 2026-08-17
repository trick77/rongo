package retrieve

import "testing"

func hitsOf(ids ...int64) []Hit {
	out := make([]Hit, len(ids))
	for i, id := range ids {
		out[i] = Hit{ChunkID: id, Repo: "r", Path: "p"}
	}
	return out
}

func TestFuseWeighted_aLiteralMatchOutranksACloserSemanticHit(t *testing.T) {
	// Given: the semantic lane put chunk 2 at rank 0, the keyword lane found
	// chunk 1 only at rank 3. Unweighted RRF would rank 2 first (1/60 beats
	// 1/63) — which is the whole failure the weights exist to prevent.
	lanes := []Lane{
		{Name: "semantic:0", Hits: hitsOf(2, 3, 4, 5), Weight: WeightSemantic},
		{Name: "keyword:strict", Hits: hitsOf(6, 7, 8, 1), Weight: WeightKeywordStrict},
	}

	// When
	got := FuseWeighted(lanes, 8)

	// Then: chunk 1 (keyword, rank 3) must come out above chunk 2 (semantic,
	// rank 0). Flatten the weights and the order inverts, because 1/60 beats
	// 1/63 — that inversion is the bug these constants exist to prevent.
	if rankOf(got, 1) > rankOf(got, 2) {
		t.Errorf("chunk 2 (semantic, rank 0) outranks chunk 1 (keyword, rank 3): %v — the lane weights were flattened",
			ids(got))
	}
}

func rankOf(hits []Hit, id int64) int {
	for i, h := range hits {
		if h.ChunkID == id {
			return i
		}
	}
	return len(hits)
}

func ids(hits []Hit) []int64 {
	out := make([]int64, len(hits))
	for i, h := range hits {
		out[i] = h.ChunkID
	}
	return out
}

func TestFuseWeighted_recordsScoreAndLanes(t *testing.T) {
	// Given: one chunk found by both lanes.
	lanes := []Lane{
		{Name: "semantic:0", Hits: hitsOf(1), Weight: WeightSemantic},
		{Name: "keyword:strict", Hits: hitsOf(1), Weight: WeightKeywordStrict},
	}

	// When
	got := FuseWeighted(lanes, 5)

	// Then: a caller cannot recover either afterwards, and "which lane found
	// this" is what separates a literal match from a semantic guess.
	if len(got) != 1 {
		t.Fatalf("got %d hits, want the two lanes fused into one", len(got))
	}
	if got[0].Score <= 0 {
		t.Errorf("Score = %v, want the fused score written back onto the hit", got[0].Score)
	}
	if len(got[0].Lanes) != 2 {
		t.Errorf("Lanes = %v, want both lanes recorded", got[0].Lanes)
	}
}

func TestFuseWeighted_mutesANonPositiveWeight(t *testing.T) {
	// Given: zero is the value a caller reaches for to silence a lane. Promoting
	// it to full confidence would make it shout loudest instead.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: hitsOf(1), Weight: 0},
		{Name: "semantic:0", Hits: hitsOf(2), Weight: WeightSemantic},
	}

	// When
	got := FuseWeighted(lanes, 5)

	// Then
	if len(got) != 1 || got[0].ChunkID != 2 {
		t.Errorf("FuseWeighted() = %v, want only the semantic lane's hit", got)
	}
}

func TestFuseWeighted_keepsTheDistanceFromWhicheverLaneHasOne(t *testing.T) {
	// Given: the keyword lane leaves Distance 0, the vector lane brings a real
	// one. A chunk found by both should end up with it.
	withDistance := hitsOf(1)
	withDistance[0].Distance = 0.42
	lanes := []Lane{
		{Name: "keyword:strict", Hits: hitsOf(1), Weight: WeightKeywordStrict},
		{Name: "semantic:0", Hits: withDistance, Weight: WeightSemantic},
	}

	// When
	got := FuseWeighted(lanes, 5)

	// Then
	if got[0].Distance != 0.42 {
		t.Errorf("Distance = %v, want 0.42 carried over from the vector lane", got[0].Distance)
	}
}

func TestFuseWeighted_noLanesIsEmptyNotNil(t *testing.T) {
	// Given / When
	got := FuseWeighted(nil, 5)

	// Then
	if len(got) != 0 {
		t.Errorf("FuseWeighted(nil) = %v, want no hits", got)
	}
}
