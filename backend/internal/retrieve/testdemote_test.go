package retrieve

import "testing"

// pathHits builds one lane's worth of hits at the given paths, all from the
// same repository.
func pathHits(repo string, paths ...string) []Hit {
	out := make([]Hit, len(paths))
	for i, p := range paths {
		out[i] = Hit{ChunkID: int64(i + 1), Repo: repo, Path: p}
	}
	return out
}

func TestFuseWeightedDecayed_dropsATestOutOfTheCut(t *testing.T) {
	// Given: a test file ranks above a second production file.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: pathHits("loom",
			"internal/llm/client.go",
			"internal/llm/client_test.go",
			"internal/httpapi/sse.go",
		), Weight: WeightKeywordStrict},
	}

	// When: two hits are wanted and the test decay is on.
	got := FuseWeightedDecayed(lanes, 2, Decays{Repo: DefaultRepoDecay, Test: 0.35})

	// Then: the production file that ranked below the test takes its place.
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2", len(got))
	}
	for _, h := range got {
		if IsTestPath(h.Path) {
			t.Fatalf("a test survived the cut: %q", h.Path)
		}
	}
}

func TestFuseWeightedDecayed_demotionShowsInTheScore(t *testing.T) {
	// Given: one test hit and one production hit, at the same lane rank.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: pathHits("loom", "a/thing.go"), Weight: WeightKeywordStrict},
		{Name: "keyword:content", Hits: pathHits("loom", "a/thing_test.go"), Weight: WeightKeywordStrict},
	}

	// When: fused with the demotion on.
	got := FuseWeightedDecayed(lanes, 2, Decays{Repo: DefaultRepoDecay, Test: 0.5})

	// Then: the score the caller reads carries the demotion — the routing
	// floor decides on it, so it cannot be an ordering trick the score hides.
	var test, prod float64
	for _, h := range got {
		if IsTestPath(h.Path) {
			test = h.Score
		} else {
			prod = h.Score
		}
	}
	if test >= prod {
		t.Fatalf("test scored %v, production %v — the demotion is not in the score", test, prod)
	}
}

func TestFuseWeightedDecayed_decayOfOneLeavesTheScoresAlone(t *testing.T) {
	// Given: the same mixed lane.
	lanes := []Lane{
		{Name: "keyword:strict", Hits: pathHits("loom", "a/thing.go", "a/thing_test.go"), Weight: WeightKeywordStrict},
	}

	// When: the test decay is switched off.
	plain := FuseWeightedDecayed(lanes, 5, Decays{Repo: DefaultRepoDecay, Test: 1.0})

	// Then: nothing moved and nothing was scaled. The knob's off position has
	// to be the behaviour that shipped.
	if len(plain) != 2 || plain[0].Path != "a/thing.go" || plain[1].Path != "a/thing_test.go" {
		t.Fatalf("decay 1.0 reordered the list: %v", plain)
	}
	if plain[0].Score <= 0 || plain[1].Score <= 0 {
		t.Fatalf("decay 1.0 must not scale a score: %v, %v", plain[0].Score, plain[1].Score)
	}
}
