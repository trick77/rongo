package edges

import (
	"context"
	"database/sql"
)

// Neighbour is a file on the far side of an integration edge: it names the same
// queue or route as the file asked about, and lives in a DIFFERENT repository.
type Neighbour struct {
	Repo  string
	Path  string
	Kind  Kind
	Value string
	Line  int
}

// spreadCeiling is how many repositories may share a token before it stops
// counting as an edge. A value present in five of eight repositories is a
// convention, not a link — "/orders" in a corpus of order services would still
// be one, but "/login" in every front-end is not — and joining on it would pull
// unrelated code into an answer that then cites it.
//
// Three is deliberately tight. The failure mode it protects against (a wrong
// edge, silently cited) is worse than the one it causes (a missing edge, which
// leaves retrieval exactly where it was before this table existed).
const spreadCeiling = 3

// Neighbours returns the files in other repositories that share an integration
// token with the given file.
//
// Same-repository matches are excluded on purpose. Inside one repository the
// symbol walk and the keyword lane already connect a caller to its callee; the
// gap this closes is the one that has no symbol, no import and no type in
// common, which only appears across a repository boundary.
func Neighbours(ctx context.Context, db *sql.DB, repo, path string) ([]Neighbour, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT other_f.repo, other_f.path, other.kind, other.value, other.line
		FROM files me
		JOIN integration_tokens mine ON mine.file_id = me.id
		JOIN integration_tokens other
		  ON other.kind = mine.kind AND other.value = mine.value
		JOIN files other_f ON other_f.id = other.file_id
		-- Enabled only, and in the COUNT as well as the join. Filtering just
		-- the result would keep a parked repository out of an answer while
		-- still letting it shape one: its tokens would count towards the
		-- spread ceiling, push a value over it, and cost a LIVE repository an
		-- edge it should have had. Same rule, and same reasoning, as the
		-- reference walk in internal/ask.
		JOIN repo_state other_r ON other_r.name = other_f.repo AND other_r.enabled = 1
		WHERE me.repo = ? AND me.path = ?
		  AND other_f.repo <> me.repo
		  AND (
		    SELECT COUNT(DISTINCT f2.repo)
		    FROM integration_tokens t2
		    JOIN files f2 ON f2.id = t2.file_id
		    JOIN repo_state r2 ON r2.name = f2.repo AND r2.enabled = 1
		    WHERE t2.kind = mine.kind AND t2.value = mine.value
		  ) <= ?
		ORDER BY other_f.repo, other_f.path, other.line`, repo, path, spreadCeiling)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Neighbour
	for rows.Next() {
		var n Neighbour
		var kind string
		if err := rows.Scan(&n.Repo, &n.Path, &kind, &n.Value, &n.Line); err != nil {
			return nil, err
		}
		n.Kind = Kind(kind)
		out = append(out, n)
	}
	return out, rows.Err()
}
