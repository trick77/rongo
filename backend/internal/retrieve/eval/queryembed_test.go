package eval

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/retrieve"
)

// queryHashPrefix sets a query's key apart from every chunk's. A content hash
// is bare hex, so a prefixed key can never read or overwrite a chunk's vector.
const queryHashPrefix = "query:"

// queryHash keys one query text in embed_cache.
func queryHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return queryHashPrefix + hex.EncodeToString(sum[:])
}

// queryCache embeds query text through embed_cache, so a rerun of a fixed
// question set pays for each question once instead of on every run. The
// product never caches a query; only the harness asks the same ones again.
type queryCache struct {
	inner retrieve.Embedder
	cache *embed.Cache
}

func newQueryCache(inner retrieve.Embedder, db *sql.DB) *queryCache {
	return &queryCache{inner: inner, cache: embed.NewCache(db, embed.Model, embed.Dim())}
}

// Embed returns one vector per text in input order, embedding only the texts
// the cache lacks, each once.
func (q *queryCache) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	hashes := make([]string, len(texts))
	for i, text := range texts {
		hashes[i] = queryHash(text)
	}
	have, err := q.cache.Get(ctx, hashes)
	if err != nil {
		return nil, err
	}
	var missing []string
	queued := map[string]bool{}
	for i, text := range texts {
		if _, ok := have[hashes[i]]; ok || queued[hashes[i]] {
			continue
		}
		queued[hashes[i]] = true
		missing = append(missing, text)
	}
	if len(missing) > 0 {
		vecs, err := q.inner.Embed(ctx, missing)
		if err != nil {
			return nil, err
		}
		if len(vecs) != len(missing) {
			return nil, fmt.Errorf("query cache: %d vectors for %d texts", len(vecs), len(missing))
		}
		for i, text := range missing {
			h := queryHash(text)
			if err := q.cache.Put(ctx, h, vecs[i]); err != nil {
				return nil, err
			}
			have[h] = vecs[i]
		}
	}
	out := make([][]float32, len(texts))
	for i, h := range hashes {
		out[i] = have[h]
	}
	return out, nil
}

// evalQueryEmbedder is the embedder every search in the harness uses: the
// product's client behind the query cache in the eval database.
func evalQueryEmbedder(t *testing.T, db *sql.DB) retrieve.Embedder {
	t.Helper()
	return newQueryCache(evalEmbedder(t), db)
}
