package retrieve

import (
	"testing"

	"github.com/trick77/rongo/internal/modules"
)

// corpus is the shape the phase-2 measurement recorded as the standing failure:
// the answering file sits in a package where several files score, while a
// single neighbour from an unrelated package outranks each of them on its own.
//
// Hits are given in the order Search returns them, best first.
func corpus() []Hit {
	return []Hit{
		{Repo: "peeq", Path: "backend/internal/sched/jitter.go", Score: 0.90},
		{Repo: "peeq", Path: "backend/internal/rag/hybrid.go", Score: 0.80},
		{Repo: "peeq", Path: "backend/internal/rag/store.go", Score: 0.75},
		{Repo: "peeq", Path: "backend/internal/cookie/netscape.go", Score: 0.50},
	}
}

func corpusModules() []modules.Module {
	return []modules.Module{
		{Repo: "peeq", Key: "backend/internal/rag", Paths: []string{
			"backend/internal/rag/hybrid.go", "backend/internal/rag/store.go",
		}},
		{Repo: "peeq", Key: "backend/internal/sched", Paths: []string{
			"backend/internal/sched/jitter.go",
		}},
		{Repo: "peeq", Key: "backend/internal/cookie", Paths: []string{
			"backend/internal/cookie/netscape.go",
		}},
	}
}

func paths(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Path
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRerankByModule_aModuleWithSeveralHitsOutranksALoneNeighbour(t *testing.T) {
	// Given
	idx := NewModuleIndex(corpusModules())

	// When
	got := RerankByModule(corpus(), idx, RerankOpts{Score: ScoreSum})

	// Then: rag's two hits together (1.55) beat sched's single 0.90, so the
	// answering file rises above the neighbour that outscored it alone.
	want := []string{
		"backend/internal/rag/hybrid.go",
		"backend/internal/rag/store.go",
		"backend/internal/sched/jitter.go",
		"backend/internal/cookie/netscape.go",
	}
	if !equal(paths(got), want) {
		t.Fatalf("order = %v, want %v", paths(got), want)
	}
}

func TestRerankByModule_scoreVariantsAreNotInterchangeable(t *testing.T) {
	// A guard against a measurement that compares two arms which are the same
	// by construction: if every variant produced this order, the comparison in
	// the eval harness would report a difference of zero no matter what the
	// code did.
	idx := NewModuleIndex(corpusModules())

	bySum := paths(RerankByModule(corpus(), idx, RerankOpts{Score: ScoreSum}))
	byBest := paths(RerankByModule(corpus(), idx, RerankOpts{Score: ScoreBest}))

	if equal(bySum, byBest) {
		t.Fatalf("ScoreSum and ScoreBest both produced %v — the variants cannot be compared", bySum)
	}
	// ScoreBest keeps sched in front: its single hit is the strongest one.
	if byBest[0] != "backend/internal/sched/jitter.go" {
		t.Errorf("ScoreBest order = %v, want the strongest single hit first", byBest)
	}
}

func TestRerankByModule_hitsOutsideEveryModuleKeepTheirOwnStanding(t *testing.T) {
	// Given: two hits from a repository that has not been clustered yet. Each
	// scores below cookie's single hit. Lumped together into one phantom module
	// they would total 0.60 and jump ahead of it — so this fixture fails loudly
	// if unclustered files are treated as one group.
	idx := NewModuleIndex(corpusModules())
	hits := append(corpus(),
		Hit{Repo: "loom", Path: "backend/internal/chat/share_store.go", Score: 0.30},
		Hit{Repo: "loom", Path: "backend/internal/artifact/upload_path.go", Score: 0.30},
	)

	// When
	got := RerankByModule(hits, idx, RerankOpts{Score: ScoreSum})

	// Then
	if len(got) != len(hits) {
		t.Fatalf("got %d hits, want %d — reranking must never drop a hit", len(got), len(hits))
	}
	at := func(path string) int {
		for i, h := range got {
			if h.Path == path {
				return i
			}
		}
		t.Fatalf("hit %s disappeared from %v", path, paths(got))
		return -1
	}
	cookie := at("backend/internal/cookie/netscape.go")
	if cookie > at("backend/internal/chat/share_store.go") || cookie > at("backend/internal/artifact/upload_path.go") {
		t.Errorf("order = %v, want cookie ahead of both strays — unclustered files must stand alone, not form a module", paths(got))
	}
}

func TestRerankByModule_sameRepoBoundaryIsRespected(t *testing.T) {
	// Given: loom has a file at the very path peeq's rag module claims. A
	// module key alone is not an identity — repo plus path is.
	idx := NewModuleIndex(corpusModules())
	hits := []Hit{
		{Repo: "loom", Path: "backend/internal/rag/hybrid.go", Score: 0.60},
		{Repo: "loom", Path: "backend/internal/rag/store.go", Score: 0.55},
		{Repo: "peeq", Path: "backend/internal/sched/jitter.go", Score: 0.90},
	}

	// When
	got := RerankByModule(hits, idx, RerankOpts{Score: ScoreSum})

	// Then: loom's two hits must not be summed into peeq's rag module and
	// thereby promoted past it.
	if got[0].Path != "backend/internal/sched/jitter.go" {
		t.Errorf("order = %v, want peeq's module first — loom's paths are not peeq's module", paths(got))
	}
}
