package retrieve

import (
	"math"
	"sort"

	"github.com/trick77/rongo/internal/modules"
)

// The phase-2 measurement left one failure standing under both embedding
// models, and three of its four questions share a shape: the answering file is
// one of several in a package that all discuss the same subject, and the right
// one loses to its neighbours. Recall was not the problem — the expected file
// sat inside the top 20 for 24 of 28 questions — the ranking was.
//
// Reranking by module is the cheapest thing that addresses exactly that: a
// package where several files score is more likely to be the subject than a
// package where exactly one file scores highly by accident. It costs no model
// call, which is why it is measured before anything is generated.

// ModuleScore names how a module's standing is derived from its hits. Which one
// wins is a measurement, not an opinion — the eval harness runs the arms
// against the same question set.
type ModuleScore string

const (
	// ScoreSum adds the module's hit scores: several corroborating files beat
	// one strong outlier.
	ScoreSum ModuleScore = "sum"
	// ScoreBest takes the module's strongest hit, which reproduces plain
	// chunk ranking at module granularity.
	ScoreBest ModuleScore = "best"
	// ScoreCount counts the module's hits, ignoring how well they scored.
	ScoreCount ModuleScore = "count"
	// ScoreBlend keeps Search's ordering of individual hits and only adds a
	// corroboration bonus to each hit's own score.
	//
	// The three variants above all reorder WHOLESALE: every hit of the
	// best-standing module moves to the front. Measured against the question
	// set that is far worse than plain chunk ranking (recall@5 0.214 against
	// 0.679), and the mechanism is visible in the module list — httpapi holds
	// 1135 chunks, so a big module wins on sheer size and buries a single
	// excellent hit from elsewhere under twenty mediocre ones.
	ScoreBlend ModuleScore = "blend"
)

// RerankOpts carries the choice of module score.
type RerankOpts struct {
	Score ModuleScore
	// Alpha is how strongly ScoreBlend weighs corroboration. 0 disables the
	// bonus entirely, which reproduces Search's own order.
	Alpha float64
}

// ModuleIndex maps an indexed file to the module that owns it. The key is repo
// plus path: two repositories routinely share a path, and summing loom's hits
// into peeq's module would promote a module on evidence that is not its own.
type ModuleIndex struct {
	byFile map[string]string
}

// NewModuleIndex builds the lookup from a clustering result.
func NewModuleIndex(mods []modules.Module) *ModuleIndex {
	idx := &ModuleIndex{byFile: make(map[string]string)}
	for _, m := range mods {
		for _, p := range m.Paths {
			idx.byFile[fileKey(m.Repo, p)] = fileKey(m.Repo, m.Key)
		}
	}
	return idx
}

func fileKey(repo, path string) string { return repo + "\x00" + path }

// group returns the module a hit belongs to. A hit whose file is in no module —
// a repository not clustered yet — stands for itself rather than being lumped
// with other strays, which would invent a module out of unrelated files.
func (idx *ModuleIndex) group(h Hit) string {
	if idx != nil {
		if k, ok := idx.byFile[fileKey(h.Repo, h.Path)]; ok {
			return k
		}
	}
	return fileKey(h.Repo, h.Path)
}

// RerankByModule reorders hits so that every hit of the best-standing module
// comes first, then the next module, and so on. Within a module the original
// order is kept.
//
// No hit is dropped and none is added: this changes the order Search already
// produced and nothing else, so a comparison against the unreranked arm sees a
// difference in ranking rather than in recall.
func RerankByModule(hits []Hit, idx *ModuleIndex, o RerankOpts) []Hit {
	if len(hits) == 0 {
		return hits
	}

	score := map[string]float64{}
	first := map[string]int{}
	count := map[string]int{}
	for i, h := range hits {
		g := idx.group(h)
		if _, seen := first[g]; !seen {
			first[g] = i
		}
		count[g]++
		switch o.Score {
		case ScoreBest:
			if h.Score > score[g] {
				score[g] = h.Score
			}
		case ScoreCount:
			score[g]++
		default: // ScoreSum
			score[g] += h.Score
		}
	}

	out := append([]Hit{}, hits...)

	if o.Score == ScoreBlend {
		// Corroboration is damped with a logarithm and counts only the hits
		// BESIDES this one, so a lone hit gets no bonus at all and a module
		// with twenty hits does not get twenty times the standing of one with
		// two. Multiplicative, because the fused RRF scores have no meaningful
		// absolute scale to add a constant to.
		boost := make(map[string]float64, len(count))
		for g, n := range count {
			boost[g] = 1 + o.Alpha*math.Log1p(float64(n-1))
		}
		blended := make([]float64, len(out))
		for i, h := range out {
			blended[i] = h.Score * boost[idx.group(h)]
		}
		order := make([]int, len(out))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			return blended[order[a]] > blended[order[b]]
		})
		ranked := make([]Hit, len(out))
		for i, j := range order {
			ranked[i] = out[j]
		}
		return ranked
	}

	sort.SliceStable(out, func(a, b int) bool {
		ga, gb := idx.group(out[a]), idx.group(out[b])
		if ga == gb {
			return false // stable: keep Search's order inside a module
		}
		if score[ga] != score[gb] {
			return score[ga] > score[gb]
		}
		// Equal standing is broken by which module Search ranked first, so the
		// result stays deterministic instead of depending on map iteration.
		return first[ga] < first[gb]
	})
	return out
}
