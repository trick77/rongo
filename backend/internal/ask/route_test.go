package ask

import (
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
)

// moduleByDir is the mapping the real router gets from internal/modules: a
// path's directory is its module.
func moduleByDir(_ string, p string) string {
	if i := lastSlash(p); i >= 0 {
		return p[:i]
	}
	return "."
}

func TestCandidatesGroupByRepoAndScoreAsTheirBestHit(t *testing.T) {
	// Given a big module with many mediocre hits and a small one with a single
	// very good hit. Phase 3 measured what summing does here: peeq's httpapi
	// has 1135 chunks and buries one excellent hit under twenty average ones.
	hits := []retrieve.Hit{
		{ChunkID: 1, Repo: "peeq", Path: "backend/internal/httpapi/a.go", Score: 0.30},
		{ChunkID: 2, Repo: "peeq", Path: "backend/internal/httpapi/b.go", Score: 0.28},
		{ChunkID: 3, Repo: "peeq", Path: "backend/internal/httpapi/c.go", Score: 0.27},
		{ChunkID: 4, Repo: "peeq", Path: "backend/internal/download/freebytes.go", Score: 0.60},
	}

	// When
	got := candidates(hits, moduleByDir)

	// Then
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].ModuleKey != "backend/internal/download" {
		t.Errorf("leading candidate is %q, want the module with the best single hit", got[0].ModuleKey)
	}
	if got[0].Score != 0.60 {
		t.Errorf("score = %v, want the best hit's score", got[0].Score)
	}
	if len(got[1].Hits) != 3 {
		t.Errorf("second candidate carries %d hits, want all 3 of its own", len(got[1].Hits))
	}
}

func TestDominatesComparesTheTopTwo(t *testing.T) {
	cs := []Candidate{{Score: 0.60}, {Score: 0.40}}

	// (0.60-0.40)/0.60 = 0.33
	if !dominates(cs, 0.25) {
		t.Error("a third clear of the runner-up must run on silently")
	}
	if dominates(cs, 0.50) {
		t.Error("under a stricter margin the same pair must be asked about")
	}
	if !dominates([]Candidate{{Score: 0.6}}, 0.25) {
		t.Error("a single candidate has nothing to be ambiguous with")
	}
	if !dominates(nil, 0.25) {
		t.Error("no candidates is the nothing-found path, not a clarification")
	}
}
