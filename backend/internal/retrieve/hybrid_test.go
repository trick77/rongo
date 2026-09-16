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

// idOrders is the pair of ids any ranking test hands a tie: one way round and
// then the other, so a result that matches the addresses by accident of the
// ids fails the second case.
var idOrders = []struct {
	name            string
	earlyID, lateID int64
}{
	{"the earlier address holds the higher id", 99, 1},
	{"the ids swapped, the order does not", 1, 99},
}

// lessByAddress is what keeps a re-index from reordering a result: chunk ids
// are handed out in index order, so a tie broken on one moves whenever the
// corpus is indexed again. A chunk's address does not move.
func TestLessByAddress_ordersOnRepoThenPathThenLineThenID(t *testing.T) {
	cases := []struct {
		name string
		a, b Hit
		want bool
	}{
		{"the repository decides first",
			Hit{Repo: "a", Path: "z.go", StartLine: 9, ChunkID: 9},
			Hit{Repo: "b", Path: "a.go", StartLine: 1, ChunkID: 1}, true},
		{"the path decides inside a repository",
			Hit{Repo: "a", Path: "a.go", StartLine: 9, ChunkID: 9},
			Hit{Repo: "a", Path: "z.go", StartLine: 1, ChunkID: 1}, true},
		{"the start line decides inside a file",
			Hit{Repo: "a", Path: "a.go", StartLine: 1, ChunkID: 9},
			Hit{Repo: "a", Path: "a.go", StartLine: 9, ChunkID: 1}, true},
		{"the ordinal decides between two chunks starting on one line",
			Hit{Repo: "a", Path: "a.go", StartLine: 1, Ordinal: 0, ChunkID: 9},
			Hit{Repo: "a", Path: "a.go", StartLine: 1, Ordinal: 1, ChunkID: 1}, true},
		{"the id is the last resort",
			Hit{Repo: "a", Path: "a.go", StartLine: 1, ChunkID: 1},
			Hit{Repo: "a", Path: "a.go", StartLine: 1, ChunkID: 9}, true},
		{"the same address, the other way round",
			Hit{Repo: "a", Path: "a.go", StartLine: 1, ChunkID: 9},
			Hit{Repo: "a", Path: "a.go", StartLine: 1, ChunkID: 1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lessByAddress(c.a, c.b); got != c.want {
				t.Errorf("lessByAddress(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestFuseWeightedDecayed_equalScoresOrderByAddressNotByID is the noise the
// flow corpus measured: two equally scored chunks came out in whatever order
// the indexer happened to hand out their ids, so the answer moved after a
// re-index that changed no code.
func TestFuseWeightedDecayed_equalScoresOrderByAddressNotByID(t *testing.T) {
	for _, st := range idOrders {
		t.Run(st.name, func(t *testing.T) {
			// Given: two chunks scored identically — each at rank 0 of one of
			// two equally weighted lanes — in one order of ids and then the
			// other.
			early := Hit{ChunkID: st.earlyID, Repo: "peeq", Path: "a.go", StartLine: 1}
			late := Hit{ChunkID: st.lateID, Repo: "peeq", Path: "z.go", StartLine: 1}

			// When
			got := FuseWeightedDecayed([]Lane{
				{Name: "keyword:strict", Hits: []Hit{late}, Weight: WeightKeywordStrict},
				{Name: "keyword:content", Hits: []Hit{early}, Weight: WeightKeywordStrict},
			}, 5, Decays{})

			// Then: a.go first, whichever id it carries.
			if len(got) != 2 {
				t.Fatalf("fused %d hits, want 2", len(got))
			}
			if got[0].Score != got[1].Score {
				t.Fatalf("the fixture stopped being a tie: %v against %v", got[0].Score, got[1].Score)
			}
			if got[0].Path != "a.go" {
				t.Errorf("fused order is %v then %v, want a.go first — the tie broke on the chunk id",
					got[0].Path, got[1].Path)
			}
		})
	}
}
