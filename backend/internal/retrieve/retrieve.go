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
	// Texts replaces Text when set: the question phrased several ways, each
	// getting its own semantic lane before fusion. The understanding step fills
	// it with the business-language restatement and the guessed code
	// vocabulary, which is the bridge a raw question does not build — "Apple TV"
	// never embeds near "AirPlay" on its own.
	//
	// The raw question belongs in here too, first. A model's guess is a guess,
	// and a wrong one must not be able to replace what was actually asked.
	Texts []string
	// Repos restricts the search. Empty means the whole corpus.
	Repos []string
	// K is how many hits to return. Zero means 10.
	K int
}

// texts is what the lanes actually search for.
func (q Query) texts() []string {
	if len(q.Texts) > 0 {
		return q.Texts
	}
	return []string{q.Text}
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
	// RepoDecay demotes a repository's repeated hits so a second repository
	// has room in the cut; see FuseWeightedDiverse. 1 is off, which is what
	// ships until the evaluation names a value.
	RepoDecay float64
}

// New builds a Retriever with the default bounds.
func New(db *sql.DB, embedder Embedder) *Retriever {
	return &Retriever{
		store:       NewStore(db),
		embedder:    embedder,
		MaxDistance: DefaultMaxDistance,
		Candidates:  defaultCandidates,
		RepoDecay:   DefaultRepoDecay,
	}
}

// Search runs both lanes and fuses them.
//
// No match anywhere returns an EMPTY SLICE and NO ERROR. "No hit means no hit"
// is an answer the caller reports along with the terms it tried; an error here
// would be indistinguishable from a broken database, and the answer layer would
// have to guess which one it was looking at.
func (r *Retriever) Search(ctx context.Context, q Query) ([]Hit, error) {
	repos, err := r.knownRepos(ctx, q.Repos)
	if err != nil {
		return nil, err
	}
	return r.searchTexts(ctx, q.texts(), repos, q.K)
}

// knownRepos drops names no repository in the index carries.
//
// The restriction is a guess: the understanding step reads it off the wording,
// and measured over the real question catalogue three of nine guesses named
// something that does not exist — "peeqs", the possessive form of peeq;
// "Peek", a plain mishearing; and "asg017/sqlite-vec", a module nobody
// indexed. A name like that is not a narrowing, it is a wipe. It goes into
// `WHERE f.repo IN (…)`, nothing can match, and the turn reports "nothing
// found" about code that is sitting in the index.
//
// So an unknown name is dropped, and a restriction left with nothing at all
// becomes no restriction — the whole corpus, exactly as before this field
// existed. A name the index DOES know still restricts, empty result and all:
// "no hit means no hit" stays true for a repository that really is empty.
func (r *Retriever) knownRepos(ctx context.Context, want []string) ([]string, error) {
	if len(want) == 0 {
		return nil, nil
	}
	rows, err := r.store.db.QueryContext(ctx,
		`SELECT name FROM repo_state WHERE name IN (`+placeholders(len(want))+`)`, toAny(want)...)
	if err != nil {
		return nil, fmt.Errorf("resolve repository restriction: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("resolve repository restriction: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve repository restriction: %w", err)
	}
	return out, nil
}

// searchTexts is Search over SEVERAL phrasings of one question.
//
// Phase 2 shaped it this way in advance, and phase 4 fills it: the understanding
// step hands in the raw question, the business-language restatement and the
// guessed code vocabulary, and each becomes its own semantic lane before fusion.
// That arrived as another lane rather than as a reshaping of this function,
// which is what the slice was for.
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

	fused := FuseWeightedDiverse(lanes, k, r.RepoDecay)
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
