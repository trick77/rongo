package retrieve

import (
	"math"
	"sort"
)

// rrfK is the Reciprocal Rank Fusion damping constant. 60 is the widely used
// default (Cormack et al.): large enough that top ranks do not dominate, small
// enough that deep ranks still contribute.
const rrfK = 60

// Lane weights. RRF on its own is rank-blind: a hit at rank 0 of one list
// scores exactly as much as a hit at rank 0 of another, however weak that
// second list is. A KNN lane ALWAYS returns its k nearest rows — on a narrow
// query most of them are simply the least distant of the irrelevant — so an
// unweighted fusion lets semantic noise outrank a literal keyword match: a
// vector hit at rank 3 scores 1/63 and beats an exact term match at FTS rank 5
// on 1/65.
//
// In rongo the asymmetry is sharper than in a transcript search. The keyword
// lane is what finds PromoMailJob when the user typed the identifier; the
// semantic lane is what finds it when they typed "Teaser-Mail". A chunk that
// literally contains the user's word is the stronger evidence, so the keyword
// lane sits above the semantic one — except at its recall floor, where
// "contains any one of these words" is weaker than "the embedding placed this
// near the question".
const (
	WeightKeywordStrict  = 1.0
	WeightKeywordContent = 0.9
	WeightKeywordPrefix  = 0.7
	WeightKeywordAny     = 0.4
	WeightSemantic       = 0.6
)

// DefaultMaxDistance is the L2 cutoff past which a semantic hit is treated as
// "not actually about this" and dropped before ranking.
//
// It is what lets a search report that it found nothing at all: without a bound
// a KNN query cannot fail — it returns k rows for any input whatsoever, and
// "no hit means no hit" would be unreachable.
//
// The vectors are unit length, so L2 and cosine rank identically and convert
// exactly: L2 = sqrt(2 - 2*cos). 1.25 is cosine ~0.22, under the ~0.3 where
// related passages start and well clear of unrelated text at cosine 0.0-0.15
// (L2 1.31-1.41). The value is peeq's, measured against ~600-token transcript
// chunks under text-embedding-3-small; rongo's chunks are enriched code and the
// model is still being chosen, so it is a CONFIGURED value here
// (BACKEND_SEARCH_MAX_DISTANCE) and the evaluation harness reports how many
// hits it barred per question. A recall failure that is really this constant
// must be visible as such, or the model comparison measures the constant.
const DefaultMaxDistance = 1.25

// Lane is one pre-ranked hit list plus the confidence its retrieval method
// earns. Name is carried onto every hit it contributes to, so a result can say
// which lanes found it.
type Lane struct {
	Name   string
	Hits   []Hit
	Weight float64
}

// FuseWeighted merges pre-ranked hit lists via weighted Reciprocal Rank Fusion:
// a hit's score is the sum over lanes of weight/(rrfK + rank). Hits are
// identified across lanes by chunk id. Returns up to k hits, best first; ties
// break by chunk id for determinism.
//
// A lane whose weight is zero or negative is MUTED rather than promoted to full
// confidence — the one value a caller would reach for to silence a lane must
// not be the value that makes it shout loudest.
//
// The test demotion is included, at DefaultTestDecay: it ships on, so the
// plainest-named fusion has to be the one the product runs. Pure weighted RRF
// is FuseWeightedDiverseTests(lanes, k, DefaultRepoDecay, 1.0).
func FuseWeighted(lanes []Lane, k int) []Hit {
	return FuseWeightedDiverse(lanes, k, DefaultRepoDecay)
}

// FuseWeightedDiverse is FuseWeighted with a per-repository decay applied
// before the list is cut to k: a repository's nth hit is ordered as if its
// score were score*decay^n, while the score written onto the hit stays the
// fusion score. Anything outside the open interval (0,1) is OFF — zero
// included, so a Retriever built as a struct literal rather than through New
// falls back to no decay instead of silently running the harshest setting
// there is. Retriever.TestDecay reads its zero the same way, which means a
// struct-literal Retriever also runs with the test demotion OFF while
// retrieve.New — the only constructor the product uses — runs it on. Both
// knobs default to "do nothing" in the hand-built case; New is where the
// shipped values live.
//
// It exists because the binding constraint moved from routing to retrieval.
// Measured on the mixed corpus, only 7 of 16 ambiguous questions retrieved
// BOTH of their alternatives into the top 20 — one repository filled the list,
// and the router was then asked to arbitrate between candidates that were
// never in it. Demoting a repository's repeats is the cheapest way to leave
// room for the second implementation; it cannot invent a candidate the lanes
// did not return.
//
// That 7 of 16 is the RAW question, which the product does not run: under the
// shipped query expansion the same corpus measures 12 of 16, so the constraint
// is much smaller than the premise made it look. See
// docs/measurements/2026-08-22-repo-diversity.md — which is also why the decay
// ships off.
//
// The decay is deliberately gentle rather than a hard per-repository cap: a
// question whose answer genuinely lives in twelve chunks of one repository
// must not lose eight of them to make room for a repository that has nothing
// to say.
func FuseWeightedDiverse(lanes []Lane, k int, decay float64) []Hit {
	return FuseWeightedDiverseTests(lanes, k, decay, DefaultTestDecay)
}

// FuseWeightedDiverseTests is FuseWeightedDiverse with the test demotion made
// explicit, so the evaluation harness can sweep it the way it sweeps the repo
// decay. Anything outside the open interval (0,1) is OFF.
//
// Unlike the repo decay, this one is applied to the hit's OWN Score and not
// only to the ordering key. The repo decay is a question of arrangement — the
// hit is as good as fusion said, it just steps aside — while a test is
// genuinely weaker evidence about how a mechanism works than the mechanism is.
// The routing floor in internal/ask reads this score to decide what may reach
// the clarification card, so a demotion the score hid would be a demotion that
// never happened.
func FuseWeightedDiverseTests(lanes []Lane, k int, decay, testDecay float64) []Hit {
	type agg struct {
		hit   Hit
		score float64
		lanes []string
	}
	byID := make(map[int64]*agg)
	var order []int64
	for _, lane := range lanes {
		if lane.Weight <= 0 {
			continue
		}
		for rank, h := range lane.Hits {
			a, ok := byID[h.ChunkID]
			if !ok {
				a = &agg{hit: h}
				byID[h.ChunkID] = a
				order = append(order, h.ChunkID)
			} else if a.hit.Distance == 0 && h.Distance > 0 {
				// The keyword lane leaves Distance at 0; the vector lane brings
				// a real one. A chunk both lanes found should keep it.
				a.hit.Distance = h.Distance
			}
			a.score += lane.Weight / float64(rrfK+rank)
			if lane.Name != "" && !contains(a.lanes, lane.Name) {
				a.lanes = append(a.lanes, lane.Name)
			}
		}
	}
	// Demote tests BEFORE the sort, so a demoted test loses its rank and its
	// place in the cut rather than merely being labelled.
	if testDecay > 0 && testDecay < 1 {
		for _, a := range byID {
			if IsTestPath(a.hit.Path) {
				a.score *= testDecay
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		ai, aj := byID[order[i]], byID[order[j]]
		if ai.score != aj.score {
			return ai.score > aj.score
		}
		return order[i] < order[j]
	})
	if k <= 0 {
		k = 10
	}
	ranked := make([]Hit, 0, len(order))
	for _, id := range order {
		a := byID[id]
		// The score and the lane provenance are written back onto the hit: a
		// caller ranking or explaining a result set cannot recover either
		// afterwards, and "which lane found this" is what distinguishes a
		// literal match from a semantic guess.
		a.hit.Score = a.score
		a.hit.Lanes = a.lanes
		ranked = append(ranked, a.hit)
	}
	// Diversify BEFORE the cut. Applied to an already-truncated list it could
	// only shuffle what one repository had already filled, which is the
	// failure it exists to fix.
	ranked = diversifyByRepo(ranked, decay)
	if len(ranked) > k {
		// Copied, not resliced: a Hit carries the chunk's raw text, and a
		// caller holding the top twenty must not pin the whole candidate pool
		// through the backing array.
		ranked = append(make([]Hit, 0, k), ranked[:k]...)
	}
	return ranked
}

// DefaultRepoDecay is off. The value that ships is set by the measurement in
// internal/retrieve/eval, not by argument here: the number it has to move is
// how many of the sixteen ambiguous questions retrieve both of their answers,
// and the thirty-four unique ones must not lose ground while it does.
const DefaultRepoDecay = 1.0

// DefaultTestDecay is how far a test hit's fused score is cut. Unlike the repo
// decay this one ships ON: nothing else in the product knows what a test is,
// and until this constant existed a header-capturing test fake competed with
// the client it fakes for the answer and for a place on the clarification
// card. Someone asking how a mechanism works is not asking about its harness.
//
// A demotion rather than a filter, because "how is this tested?" is a real
// question: a test that is the only thing matching still wins, it just cannot
// outrank the mechanism itself. The value is the evaluation harness's to set —
// see internal/retrieve/eval — not an argument settled here.
const DefaultTestDecay = 0.35

// diversifyByRepo reorders a ranked list so a repository's repeats step aside
// for another repository's best hit. Nothing is dropped — reordering is not
// filtering — and the input order breaks every tie, so the result is stable
// for an unchanged corpus.
//
// Quadratic in the fused list, which is bounded by the candidate pool per lane
// (forty by default). A cleverer structure would trade readability for a
// saving nobody can measure.
func diversifyByRepo(hits []Hit, decay float64) []Hit {
	if decay <= 0 || decay >= 1 || len(hits) < 2 {
		return hits
	}
	taken := make(map[string]int, 4)
	remaining := make([]Hit, len(hits))
	copy(remaining, hits)
	out := make([]Hit, 0, len(hits))
	for len(remaining) > 0 {
		best, bestScore := 0, penalised(remaining[0], taken, decay)
		for i := 1; i < len(remaining); i++ {
			// Strictly greater, so a tie keeps the better fusion rank: the
			// input is already sorted, and equal scores must not depend on map
			// iteration or on which repository was seen first.
			if sc := penalised(remaining[i], taken, decay); sc > bestScore {
				best, bestScore = i, sc
			}
		}
		h := remaining[best]
		out = append(out, h)
		taken[h.Repo]++
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return out
}

// penalised is the ordering key: the fusion score damped once per hit already
// taken from the same repository. The hit's own Score is left untouched, because
// a caller explaining a result reports what the fusion concluded, not what the
// ordering did with it afterwards.
func penalised(h Hit, taken map[string]int, decay float64) float64 {
	return h.Score * math.Pow(decay, float64(taken[h.Repo]))
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
