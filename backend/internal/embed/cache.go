package embed

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// getBatch is how many hashes go into one IN (…) lookup. A full repository
// index asks for thousands at once, which is well past SQLite's variable limit
// — a hard failure on the first real run that no small unit test reaches.
const getBatch = 500

// Cache is the content-hash embedding cache.
//
// It is keyed by (content_hash, model): the same content under a different
// model is a MISS. That is what keeps the small-vs-large comparison honest,
// and it is also what makes a re-index bearable in dev, where the corpus is
// re-indexed constantly and unchanged content must never be paid for twice.
type Cache struct {
	db    *sql.DB
	model string
	dim   int
}

// NewCache builds a Cache for one model and vector width.
func NewCache(db *sql.DB, model string, dim int) *Cache {
	return &Cache{db: db, model: model, dim: dim}
}

// Get returns the vectors this cache holds for hashes, under its own model.
// Hashes it does not hold are simply absent: the caller embeds those, and a
// zero vector returned in their place would store silence as if it were
// meaning.
func (c *Cache) Get(ctx context.Context, hashes []string) (map[string][]float32, error) {
	out := make(map[string][]float32, len(hashes))
	for start := 0; start < len(hashes); start += getBatch {
		end := min(start+getBatch, len(hashes))
		batch := hashes[start:end]
		args := make([]any, 0, len(batch)+1)
		args = append(args, c.model)
		for _, h := range batch {
			args = append(args, h)
		}
		q := `SELECT content_hash, embedding FROM embed_cache WHERE model = ? AND content_hash IN (` +
			strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + `)`
		rows, err := c.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("embed cache get: %w", err)
		}
		for rows.Next() {
			var hash string
			var blob []byte
			if err := rows.Scan(&hash, &blob); err != nil {
				rows.Close()
				return nil, fmt.Errorf("embed cache get: %w", err)
			}
			vec, err := decodeVector(blob)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("embed cache get %s: %w", hash, err)
			}
			out[hash] = vec
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("embed cache get: %w", err)
		}
	}
	return out, nil
}

// Put stores one vector. Re-storing a hash overwrites it rather than failing,
// because a re-index legitimately arrives at content that is already cached.
func (c *Cache) Put(ctx context.Context, hash string, vec []float32) error {
	if c.dim > 0 && len(vec) != c.dim {
		return fmt.Errorf("embed cache put %s: vector has %d dimensions, want %d", hash, len(vec), c.dim)
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO embed_cache (content_hash, model, dim, embedding) VALUES (?,?,?,?)
		 ON CONFLICT (content_hash, model) DO UPDATE SET dim = excluded.dim, embedding = excluded.embedding`,
		hash, c.model, len(vec), encodeVector(vec))
	if err != nil {
		return fmt.Errorf("embed cache put %s: %w", hash, err)
	}
	return nil
}

// encodeVector stores a vector as little-endian float32, the same layout vec0
// uses, so a cached vector needs no conversion on its way to the index.
func encodeVector(vec []float32) []byte {
	buf := make([]byte, 4*len(vec))
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(f))
	}
	return buf
}

func decodeVector(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("cached vector is %d bytes, not a whole number of float32", len(blob))
	}
	out := make([]float32, len(blob)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[4*i:]))
	}
	return out, nil
}
