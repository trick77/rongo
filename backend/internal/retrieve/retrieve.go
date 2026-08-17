package retrieve

import (
	"context"
	"database/sql"
	"fmt"
)

// defaultCandidates is how many rows each lane retrieves before fusion. Fusion
// can only rank what the lanes handed it, so this is the real recall ceiling;
// the caller's K only trims the fused list.
const defaultCandidates = 40

// Embedder turns query text into vectors. It is the same interface the indexer
// uses, so the query and the corpus are embedded by the same client and the
// same model — anything else compares vectors from two different spaces.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Query is one search.
type Query struct {
	Text string
	// Repos restricts the search. Empty means the whole corpus.
	Repos []string
	// K is how many hits to return. Zero means 10.
	K int
}

// Retriever answers a Query by fusing the semantic and keyword lanes.
type Retriever struct {
	store    *Store
	embedder Embedder
	// MaxDistance bounds the semantic lane; see DefaultMaxDistance. Negative
	// disables it, which makes an empty result impossible.
	MaxDistance float64
	// Candidates is how many rows each lane fetches before fusion.
	Candidates int
}

// New builds a Retriever with the default bounds.
func New(db *sql.DB, embedder Embedder) *Retriever {
	return &Retriever{
		store:       NewStore(db),
		embedder:    embedder,
		MaxDistance: DefaultMaxDistance,
		Candidates:  defaultCandidates,
	}
}

// Search runs both lanes and fuses them.
//
// No match anywhere returns an EMPTY SLICE and NO ERROR. "No hit means no hit"
// is an answer the caller reports along with the terms it tried; an error here
// would be indistinguishable from a broken database, and the answer layer would
// have to guess which one it was looking at.
func (r *Retriever) Search(ctx context.Context, q Query) ([]Hit, error) {
	return r.searchTexts(ctx, []string{q.Text}, q.Repos, q.K)
}

// searchTexts is Search over SEVERAL phrasings of one question.
//
// It takes a slice even though Query carries a single string today, because
// phase 4 embeds the expanded query twice — once in business phrasing, once
// with guessed code vocabulary — and merges both result lists. Structuring it
// this way now means that arrives as another lane rather than as a reshaping of
// this function.
func (r *Retriever) searchTexts(ctx context.Context, texts []string, repos []string, k int) ([]Hit, error) {
	if k <= 0 {
		k = 10
	}
	candidates := r.Candidates
	if candidates <= 0 {
		candidates = defaultCandidates
	}

	var usable []string
	for _, t := range texts {
		if t != "" {
			usable = append(usable, t)
		}
	}
	if len(usable) == 0 {
		return []Hit{}, nil
	}

	var lanes []Lane

	vecs, err := r.embedder.Embed(ctx, usable)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != len(usable) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d query texts", len(vecs), len(usable))
	}
	for i, v := range vecs {
		hits, err := r.store.SearchVector(ctx, v, candidates, r.MaxDistance, repos)
		if err != nil {
			return nil, err
		}
		lanes = append(lanes, Lane{
			Name:   fmt.Sprintf("semantic:%d", i),
			Hits:   hits,
			Weight: WeightSemantic,
		})
	}

	// Every rung that returns rows becomes its own lane, carrying its own
	// weight. Unlike peeq's version this does not stop descending early: there
	// the rungs are round-trips worth avoiding, here they are local SQLite
	// queries against a single file.
	for _, text := range usable {
		for _, tier := range BuildFTSQueries(text) {
			hits, err := r.store.SearchKeyword(ctx, tier.Match, candidates, repos)
			if err != nil {
				return nil, err
			}
			if len(hits) == 0 {
				continue
			}
			lanes = append(lanes, Lane{
				Name:   laneName(tier.Weight),
				Hits:   hits,
				Weight: tier.Weight,
			})
		}
	}

	fused := FuseWeighted(lanes, k)
	if fused == nil {
		// An empty slice, never nil: the caller distinguishes "nothing found"
		// from an error, not from a nil check.
		return []Hit{}, nil
	}
	return fused, nil
}

// laneName labels a keyword rung by what it means, so a result can explain
// itself: "this chunk contains every word you typed" is a different claim from
// "this chunk contains one of them".
func laneName(weight float64) string {
	switch weight {
	case WeightKeywordStrict:
		return "keyword:strict"
	case WeightKeywordContent:
		return "keyword:content"
	case WeightKeywordPrefix:
		return "keyword:prefix"
	default:
		return "keyword:any"
	}
}
