// Package retrieve answers a question against the index by fusing a vec0
// nearest-neighbour lane with an FTS5 keyword lane.
package retrieve

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/trick77/rongo/internal/store"
)

// Hit is one retrieved chunk, carrying everything a citation needs: repo,
// branch, path and line range. They are joined in the retrieval query rather
// than looked up afterwards, because every claim rongo makes must be citable
// and a second lookup is a second chance to lose the branch.
//
// ChunkID is the fusion key. Without it the two lanes cannot tell that they
// found the same chunk, and every hit would be counted once per lane.
type Hit struct {
	ChunkID   int64
	Repo      string
	Branch    string
	Path      string
	Symbol    string
	RawText   string
	StartLine int
	EndLine   int
	// Distance is the L2 distance from the query vector, set by the semantic
	// lane only. The keyword lane leaves it 0: FTS rank is positional and the
	// fusion works on rank, not on score.
	Distance float64
	// Score and Lanes are filled by FuseWeighted.
	Score float64
	Lanes []string
}

// vecKMax is vec0's own ceiling on the `k = ?` constraint (SQLITE_VEC_VEC0_K_MAX
// in sqlite-vec.c). Exceeding it is an error from the virtual table, so k is
// clamped rather than passed through.
const vecKMax = 4096

// hitColumns is the projection both lanes share, so a hit means the same thing
// whichever lane produced it.
const hitColumns = `c.id, f.repo, r.branch, f.path, c.symbol, c.raw_text, c.start_line, c.end_line`

const hitJoins = `
	JOIN chunks c ON c.id = %s.rowid
	JOIN files f ON f.id = c.file_id
	JOIN repo_state r ON r.name = f.repo`

// Store runs the two retrieval lanes against the database.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// SearchVector returns up to k chunks nearest to vec whose distance is below
// maxDistance, optionally restricted to repos. A non-positive maxDistance
// disables the bound.
//
// The two bounds in this query are different kinds of thing, and confusing them
// is the mistake this comment exists to prevent:
//
//   - maxDistance is a POST-filter. vec0 picks its k nearest rows and the bound
//     then drops the far ones, so a query with few close chunks legitimately
//     returns fewer than k — which is what makes an empty result possible at
//     all.
//   - the repository restriction is a PRE-filter. vec0 treats `rowid IN (...)`
//     as a first-class KNN constraint and ANDs the candidate rowids into the
//     validity mask BEFORE computing distances, so k applies to the filtered
//     set: "the 40 nearest chunks among peeq's", not "the 40 nearest overall,
//     of which some happen to be peeq's".
//
// Post-filtering the repository would return nothing for any repository holding
// a small slice of the corpus, which is most of them.
func (s *Store) SearchVector(ctx context.Context, vec []float32, k int, maxDistance float64, repos []string) ([]Hit, error) {
	if k <= 0 {
		k = 10
	}
	if k > vecKMax {
		k = vecKMax
	}
	inner := `SELECT ` + hitColumns + `, v.distance
		FROM chunks_vec v` + fmt.Sprintf(hitJoins, "v") + `
		WHERE v.embedding MATCH ? AND k = ?`
	args := []any{store.VecLiteral(vec), k}
	if len(repos) > 0 {
		inner += "\n\t\tAND v.rowid IN (SELECT c2.id FROM chunks c2 JOIN files f2 ON f2.id = c2.file_id" +
			" WHERE f2.repo IN (" + placeholders(len(repos)) + "))"
		args = append(args, toAny(repos)...)
	}
	q := `SELECT * FROM (` + inner + `) WHERE ? <= 0 OR distance < ? ORDER BY distance`
	args = append(args, maxDistance, maxDistance)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.Repo, &h.Branch, &h.Path, &h.Symbol,
			&h.RawText, &h.StartLine, &h.EndLine, &h.Distance); err != nil {
			return nil, fmt.Errorf("vector search: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SearchKeyword returns up to n chunks whose raw text matches the FTS5
// expression, best (lowest bm25) first. match must already have been built by
// BuildFTSMatch; an empty match yields no hits without touching the database.
//
// No vec0-style care is needed for the repository restriction here: FTS5 is not
// a top-k operator, so the join and the WHERE run before ORDER BY … LIMIT ever
// does. This is a pre-filter by construction.
//
// bm25() must name the FTS5 table itself, never the query alias — SQLite
// resolves it as a hidden column on the virtual table, where aliases are not
// recognised.
func (s *Store) SearchKeyword(ctx context.Context, match string, n int, repos []string) ([]Hit, error) {
	if strings.TrimSpace(match) == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 10
	}
	q := `SELECT ` + hitColumns + `
		FROM chunks_fts x` + fmt.Sprintf(hitJoins, "x") + `
		WHERE x.raw_text MATCH ?`
	args := []any{match}
	if len(repos) > 0 {
		q += " AND f.repo IN (" + placeholders(len(repos)) + ")"
		args = append(args, toAny(repos)...)
	}
	q += "\n\t\tORDER BY bm25(chunks_fts) LIMIT ?"
	args = append(args, n)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.Repo, &h.Branch, &h.Path, &h.Symbol,
			&h.RawText, &h.StartLine, &h.EndLine); err != nil {
			return nil, fmt.Errorf("keyword search: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
