package retrieve

import "sort"

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
func FuseWeighted(lanes []Lane, k int) []Hit {
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
	out := make([]Hit, 0, min(k, len(order)))
	for _, id := range order {
		a := byID[id]
		// The score and the lane provenance are written back onto the hit: a
		// caller ranking or explaining a result set cannot recover either
		// afterwards, and "which lane found this" is what distinguishes a
		// literal match from a semantic guess.
		a.hit.Score = a.score
		a.hit.Lanes = a.lanes
		out = append(out, a.hit)
		if len(out) >= k {
			break
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
