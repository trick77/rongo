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
// common, which only appears across a repository boundary. The one exception
// is a property key: the code's default sits in a properties file of the same
// repository, which no symbol reaches, so that kind may land at home too.
func Neighbours(ctx context.Context, db *sql.DB, repo, path string) ([]Neighbour, error) {
	return NeighboursWith(ctx, db, repo, path, Match{})
}

// Holders returns the files in ENABLED repositories that carry a token of this
// kind with exactly this value — the value-keyed half of NeighboursWith, which
// starts from a near side instead.
//
// It exists for a caller that has a name and no file to cross from: the gap
// pass in internal/ask reads the gathered sources, is told a queue name the
// mechanism uses, and has to find where that name is served. Same spread
// ceiling and same enabled-only counting as NeighboursWith, so the two halves
// of the table cannot disagree about which values are links; matching is
// exact, because a spelled name is not a near side's literal and a loose rule
// would land it on whatever route ends the same way.
func Holders(ctx context.Context, db *sql.DB, kind Kind, value string) ([]Neighbour, error) {
	if kind == KindLink {
		// A link is not an edge: see KindLink. Refused here, not left to the
		// SQL, so a caller spelling the kind gets the same nothing.
		return nil, nil
	}
	// The kind and the value are bound into the spread count rather than
	// correlated on t: the pair is the caller's, the same for every row, so
	// the count is one uncorrelated subquery evaluated once instead of per
	// candidate row.
	rows, err := db.QueryContext(ctx, `
		SELECT f.repo, f.path, t.kind, t.value, t.line
		FROM integration_tokens t
		JOIN files f ON f.id = t.file_id
		-- Enabled only, in the COUNT as well as the join, for the reason
		-- NeighboursWith gives: parked code influences nothing.
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
		WHERE t.kind = ? AND t.value = ?
		  AND `+spreadCount("?", "?")+` <= ?
		ORDER BY f.repo, f.path, t.line`,
		string(kind), value, string(kind), value, spreadCeiling)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNeighbours(rows)
}

// spreadCount is the number of ENABLED repositories carrying one kind/value
// pair, as a subquery over the expressions naming that pair. Both halves of
// the table count the same way, or they would disagree about which values are
// links — see spreadCeiling.
func spreadCount(kind, value string) string {
	return `(
		SELECT COUNT(DISTINCT f2.repo)
		FROM integration_tokens t2
		JOIN files f2 ON f2.id = t2.file_id
		JOIN repo_state r2 ON r2.name = f2.repo AND r2.enabled = 1
		WHERE t2.kind = ` + kind + ` AND t2.value = ` + value + `
	)`
}

// scanNeighbours reads the repo, path, kind, value, line shape both queries
// select.
func scanNeighbours(rows *sql.Rows) ([]Neighbour, error) {
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

// Match says how two route tokens are compared.
type Match struct {
	// Suffix lets a route match another that ENDS in it at a segment
	// boundary: the client writes cartsUrl + "/" + id + "/merge" and records
	// "/merge", the server serves "/carts/{customerId}/merge". Exact matching
	// never joins them. Off by default; measured on the flow corpus before it
	// is turned on anywhere, because a loose route rule is how "/get" joins
	// the estate to itself. Destinations are never suffix-matched.
	Suffix bool
}

// NeighboursWith is Neighbours under an explicit Match.
func NeighboursWith(ctx context.Context, db *sql.DB, repo, path string, m Match) ([]Neighbour, error) {
	same := `other.value = mine.value`
	if m.Suffix {
		// Every route starts with "/", so a shorter route that is the tail of
		// a longer one already sits at a segment boundary: "/emerge" does not
		// end in "/merge", "/x/merge" does.
		same = `(other.value = mine.value OR (mine.kind = 'route' AND (
			(length(other.value) > length(mine.value) AND substr(other.value, -length(mine.value)) = mine.value)
			OR (length(mine.value) > length(other.value) AND substr(mine.value, -length(other.value)) = other.value))))`
	}
	rows, err := db.QueryContext(ctx, `
		SELECT other_f.repo, other_f.path, other.kind, other.value, other.line
		FROM files me
		JOIN integration_tokens mine ON mine.file_id = me.id
		JOIN integration_tokens other
		  ON other.kind = mine.kind AND `+same+`
		JOIN files other_f ON other_f.id = other.file_id
		-- Enabled only, and in the COUNT as well as the join. Filtering just
		-- the result would keep a parked repository out of an answer while
		-- still letting it shape one: its tokens would count towards the
		-- spread ceiling, push a value over it, and cost a LIVE repository an
		-- edge it should have had. Same rule, and same reasoning, as the
		-- reference walk in internal/ask.
		JOIN repo_state other_r ON other_r.name = other_f.repo AND other_r.enabled = 1
		WHERE me.repo = ? AND me.path = ?
		  -- A link is not an edge (see KindLink): two user interfaces
		  -- pointing at the same portal say nothing about each other.
		  AND mine.kind <> 'link'
		  -- Other repositories only, except for a property key read by
		  -- CODE: the file that sets a key's default is a properties file
		  -- in the SAME repository as the code reading it, and a properties
		  -- file has no symbol the in-repo walk could follow. From a
		  -- properties file the exception does not apply — two stage files
		  -- of one infrastructure repository share every key, and joining
		  -- them to each other would spend the crossing reserve on nothing.
		  -- And the far side at home is a properties file, never a sibling
		  -- class reading the same key: the walk reaches those, and each
		  -- would cost a chunk of the reserve. The file itself is never its
		  -- own neighbour.
		  AND (other_f.repo <> me.repo
		       OR (mine.kind = 'property' AND other_f.path <> me.path
		           AND me.path NOT LIKE '%.properties'
		           AND other_f.path LIKE '%.properties'))
		  AND `+spreadCount("mine.kind", "mine.value")+` <= ?
		ORDER BY other_f.repo, other_f.path, other.line`, repo, path, spreadCeiling)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNeighbours(rows)
}

// InRepo returns every token of one kind in ONE enabled repository, in path
// and line order. It is the census half of the table: no value to match, no
// spread ceiling, no far side. It exists for the link kind, which never
// crosses (see KindLink) and is only ever read this way — "what does this
// user interface link to" is a list of the repository's own sites.
func InRepo(ctx context.Context, db *sql.DB, repo string, kind Kind) ([]Neighbour, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT f.repo, f.path, t.kind, t.value, t.line
		FROM integration_tokens t
		JOIN files f ON f.id = t.file_id
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
		WHERE f.repo = ? AND t.kind = ?
		ORDER BY f.path, t.line`, repo, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNeighbours(rows)
}
