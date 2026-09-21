// Package retrieve answers a question against the index by fusing a vec0
// nearest-neighbour lane with an FTS5 keyword lane.
package retrieve

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"

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
	// Ordinal is the chunk's position in its file. It completes the address:
	// an overlong line is split into sibling chunks that START on the same
	// line, so the repository, path and start line do not tell those apart and
	// only the ordinal keeps the file in file order. It is a file position and
	// not an id, so it says the same thing after a re-index.
	Ordinal int
	// SHA is the commit the file was indexed at. A citation carries it so the
	// cited lines can be shown as they were when the answer was written, not
	// as the branch has moved on since.
	SHA string
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
const hitColumns = `c.id, f.repo, r.branch, f.path, c.symbol, c.raw_text, c.start_line, c.end_line, c.ordinal, f.sha`

// The repo_state join carries enabled = 1: a repository parked with
// `enabled: false` in the YAML keeps its index and its checkout, but it answers
// nothing. Before this the flag stopped the poller and nothing else, so a
// "parked" repository went on being retrieved and cited out of an index the
// Repos page said was retired.
//
// For the KEYWORD lane this predicate is a pre-filter by construction — FTS5 is
// not a top-k operator, so the join runs before ORDER BY … LIMIT. The vector
// lane cannot use it that way; see SearchVector.
const hitJoins = `
	JOIN chunks c ON c.id = %s.rowid
	JOIN files f ON f.id = c.file_id
	JOIN repo_state r ON r.name = f.repo AND r.enabled = 1`

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
	return s.SearchVectorIn(ctx, vec, k, maxDistance, repos, nil)
}

// StagePrefixes narrows the repositories that declare stages to the asked
// stage's directory: repository name to the path prefix its files must
// carry. A repository absent from the map is not narrowed at all — the
// service repository beside an infrastructure one keeps every file — and a
// repository present with a prefix nothing starts with contributes nothing,
// which is what asking for a stage a repository does not have means.
type StagePrefixes map[string]string

// clause is the SQL restriction over f's repo and path, with its arguments,
// or empty when nothing is narrowed. substr rather than LIKE, so "_" in a
// directory name is a character and not a wildcard.
func (p StagePrefixes) clause(alias string) (string, []any) {
	if len(p) == 0 {
		return "", nil
	}
	repos := make([]string, 0, len(p))
	for r := range p {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	var args []any
	q := " AND (" + alias + ".repo NOT IN (" + placeholders(len(repos)) + ")"
	args = append(args, toAny(repos)...)
	for _, r := range repos {
		// length() in SQL, not len() in Go: substr counts characters and
		// len counts bytes, and a prefix with an umlaut would never match.
		q += " OR (" + alias + ".repo = ? AND substr(" + alias + ".path, 1, length(?)) = ?)"
		args = append(args, r, p[r], p[r])
	}
	return q + ")", args
}

// SearchVectorIn is SearchVector under a stage restriction as well.
func (s *Store) SearchVectorIn(ctx context.Context, vec []float32, k int, maxDistance float64, repos []string, stage StagePrefixes) ([]Hit, error) {
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
	// Both restrictions ride in the SAME rowid subquery, and neither may be
	// moved into hitJoins. chunks_vec is a top-k operator: MATCH … AND k = ?
	// hands back k rows and every predicate outside it runs AFTER. A parked
	// repository holding the nearest k chunks would therefore win all k slots
	// and then be discarded, leaving a live repository with a small slice of the
	// corpus returning nothing — the same failure the repository restriction was
	// moved here to avoid. The enabled clause is unconditional; the repo list
	// only narrows it further.
	inner += "\n\t\tAND v.rowid IN (SELECT c2.id FROM chunks c2" +
		" JOIN files f2 ON f2.id = c2.file_id" +
		" JOIN repo_state r2 ON r2.name = f2.repo" +
		" WHERE r2.enabled = 1"
	if len(repos) > 0 {
		inner += " AND f2.repo IN (" + placeholders(len(repos)) + ")"
		args = append(args, toAny(repos)...)
	}
	// The stage restriction rides in the same subquery, for the same reason.
	stageQ, stageArgs := stage.clause("f2")
	inner += stageQ
	args = append(args, stageArgs...)
	inner += ")"
	// Ties on distance are broken on the chunk's ADDRESS, never left to the
	// rowid the row happens to carry: rowids are handed out in index order, so
	// an unbroken tie reorders the result after a re-index that changed no
	// code. The names are the subquery's, so they are unqualified.
	//
	// This makes the OUTER order stable and nothing more. vec0's own top-k
	// runs inside, and which of several equidistant chunks it keeps at the k
	// boundary is its decision — a chunk cut there is not reachable from here
	// whatever this clause says.
	const outerTail = `) WHERE ? <= 0 OR distance < ?
		ORDER BY distance, repo, path, start_line, ordinal`
	q := `SELECT * FROM (` + inner + outerTail //nolint:gosec // only fixed SQL structure is interpolated; every value is a bound ? parameter
	args = append(args, maxDistance, maxDistance)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.Repo, &h.Branch, &h.Path, &h.Symbol,
			&h.RawText, &h.StartLine, &h.EndLine, &h.Ordinal, &h.SHA, &h.Distance); err != nil {
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
	return s.SearchKeywordIn(ctx, match, n, repos, nil)
}

// SearchKeywordIn is SearchKeyword under a stage restriction as well.
func (s *Store) SearchKeywordIn(ctx context.Context, match string, n int, repos []string, stage StagePrefixes) ([]Hit, error) {
	if strings.TrimSpace(match) == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 10
	}
	//nolint:gosec // only fixed SQL structure is interpolated (a ?-placeholder list or a literal table name); every value is a bound ? parameter
	q := `SELECT ` + hitColumns + `
		FROM chunks_fts x` + fmt.Sprintf(hitJoins, "x") + `
		WHERE x.raw_text MATCH ?`
	args := []any{match}
	if len(repos) > 0 {
		q += " AND f.repo IN (" + placeholders(len(repos)) + ")"
		args = append(args, toAny(repos)...)
	}
	stageQ, stageArgs := stage.clause("f")
	q += stageQ
	args = append(args, stageArgs...)
	// Address after bm25, for the reason SearchVector gives: two chunks the
	// ranking cannot separate must not be separated by their rowids, which
	// are index order.
	q += "\n\t\tORDER BY bm25(chunks_fts), f.repo, f.path, c.start_line, c.ordinal LIMIT ?"
	args = append(args, n)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.Repo, &h.Branch, &h.Path, &h.Symbol,
			&h.RawText, &h.StartLine, &h.EndLine, &h.Ordinal, &h.SHA); err != nil {
			return nil, fmt.Errorf("keyword search: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// FilesMatchingSubstrings counts, per term, the ENABLED files whose raw text
// contains it — which is how selective that term is. It is the seed rung's
// whole gate: a term in a handful of files is a claim about those files, a
// term in ninety is the name of a field every form and translation mentions.
//
// Counted over files rather than chunks on purpose. A long accessor used
// twice in one converter is one place, not two.
func (s *Store) FilesMatchingSubstrings(ctx context.Context, terms []string, repos []string, stage StagePrefixes) (map[string]int, error) {
	out := make(map[string]int, len(terms))
	var want []string
	for _, t := range terms {
		t = strings.TrimSpace(strings.ToLower(t))
		if t == "" || out[t] != 0 {
			continue
		}
		out[t] = 0
		want = append(want, t)
	}
	if len(want) == 0 {
		return out, nil
	}

	// ONE pass over the corpus for every term, not one pass per term. The scan
	// is unindexed by construction — instr() over raw text is what reaches an
	// identifier inside a larger token, and no index can answer that — so the
	// cost is the corpus, and paying it once per TURN rather than once per
	// TERM is the whole difference. Measured: 58 separate counts cost 1646
	// ms/op against a 25k-chunk corpus where the existing substring rung
	// costs 444.
	//
	// The row is counted per (term, file), so COUNT(DISTINCT) over the pairs
	// gives each term its file count in one group-by.
	//
	// Matching is the same expression SearchSubstringIn uses, including the
	// title-cased spelling for a non-ASCII term: SQLite's lower() is
	// ASCII-only, so a gate testing only the folded spelling counts zero for
	// exactly the German identifiers this rung exists for and skips the term
	// before the fetch that would have found it.
	var sel []string
	args := []any{}
	for i, t := range want {
		if hasNonASCII(t) {
			rs := []rune(t)
			titled := string(unicode.ToUpper(rs[0])) + string(rs[1:])
			sel = append(sel, fmt.Sprintf(
				"COUNT(DISTINCT CASE WHEN instr(c.raw_text, ?) > 0 OR instr(c.raw_text, ?) > 0 THEN c.file_id END) AS t%d", i))
			args = append(args, t, titled)
			continue
		}
		sel = append(sel, fmt.Sprintf(
			"COUNT(DISTINCT CASE WHEN instr(lower(c.raw_text), ?) > 0 THEN c.file_id END) AS t%d", i))
		args = append(args, t)
	}

	q := "SELECT " + strings.Join(sel, ", ") + `
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1`
	where := ""
	if len(repos) > 0 {
		where += " WHERE f.repo IN (" + placeholders(len(repos)) + ")"
		args = append(args, toAny(repos)...)
	}
	stageQ, stageArgs := stage.clause("f")
	if stageQ != "" && where == "" {
		// stage.clause emits a leading " AND"; it needs a WHERE to hang on.
		where = " WHERE 1=1"
	}
	where += stageQ
	args = append(args, stageArgs...)

	counts := make([]any, len(want))
	for i := range counts {
		counts[i] = new(int)
	}
	//nolint:gosec // only fixed SQL structure is interpolated; every value is a bound ? parameter
	if err := s.db.QueryRowContext(ctx, q+where, args...).Scan(counts...); err != nil {
		return nil, fmt.Errorf("count substring matches: %w", err)
	}
	for i, t := range want {
		out[t] = *(counts[i].(*int))
	}
	return out, nil
}

// substringHubShare is the share of the corpus past which a term is treated as
// a hub and skipped. A substring matching a fiftieth of every chunk is telling
// the fusion nothing it did not already know, and it costs a lane slot that a
// real identifier could have used.
//
// The floor on term LENGTH (minSubstringRunes) catches most of these before the
// query runs; this catches the rest — a long term that happens to be ubiquitous,
// like a package path or a licence header.
const substringHubShare = 0.02

// substringHubFloor is the corpus size below which the share guard does not
// apply. A share is meaningless over a handful of chunks: in a test fixture, or
// a corpus mid-index, the one chunk that legitimately contains the identifier IS
// a large share of the whole. Below this the term stands on its length alone.
const substringHubFloor = 500

// SearchSubstringIn is the rung that finds an identifier occurring only INSIDE a
// larger token. It is the one lane that does not go through FTS5.
//
// The keyword lane cannot answer this: chunks_fts is fts5(raw_text) with the
// default unicode61 tokenizer, so getAnzahlFahrzeuge and setAnzahlfahrzeuge are each a
// single token and the bare word "anzahlfahrzeuge" matches neither. The prefix
// rungs widen to the right, and the word is at the token's right end. Measured
// over the policenantrag corpus: FTS 0, prefix rung 0, substring 145 chunks.
//
// instr() rather than LIKE: LIKE would make % and _ in the term into syntax and
// need an ESCAPE clause, while instr takes the needle literally. Both sides are
// folded — the column by SQL lower(), the term by the caller's fold() — and
// SQLite's lower() is ASCII-only, which is why the Go side folds too rather
// than trusting the column's.
//
// This is a SCAN. It has no ranking of its own, so it cannot order by relevance
// the way bm25 does for the keyword lane; it orders by address instead, which
// keeps two runs over one database identical. Ranking is fusion's job, and the
// reranker's after it: getting the chunk INTO the fused list is all this lane
// claims to do.
func (s *Store) SearchSubstringIn(ctx context.Context, term string, n int, repos []string, stage StagePrefixes) ([]Hit, error) {
	term = strings.TrimSpace(strings.ToLower(term))
	if term == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 10
	}

	// WHAT THIS RUNG REACHES, and what it does not.
	//
	// The needle is folded to letters and digits; the haystack is RAW source.
	// So a match requires the identifier to appear in the code as one
	// unbroken run of the needle's characters, differing at most in case:
	// getAnzahlFahrzeuge, setAnzahlfahrzeuge, ANZAHLFAHRZEUGE all contain
	// "anzahlfahrzeuge" case-insensitively. That is the camelCase and PascalCase
	// case, which is what motivated the rung.
	//
	// It does NOT reach an identifier the source breaks up, because no case
	// variant of a glued needle appears in the text at all:
	//
	//   set_anzahl_fahrzeuge   — separators; BuildSubstringTerms emits the
	//                         snake_case spelling separately for this
	//   getWeitergabeFahrzeuge with needle "weitergabefahrzeuge" — SQLite's
	//                         lower() is ASCII-only, so the column cannot be
	//                         folded over the umlaut, and the two spellings
	//                         tried below only cover a leading capital, not a
	//                         non-ASCII letter mid-identifier followed by an
	//                         internal capital
	//
	// The second is a real gap, measured and left open rather than papered
	// over: closing it means folding the HAYSTACK, which needs either a
	// generated folded column (a migration and a full re-index) or a Unicode
	// lower() registered on every connection. Both are larger than this rung,
	// and neither is decided without a number.
	var match string
	var matchArgs []any
	if hasNonASCII(term) {
		// Rune-safe: the first character may itself be the multibyte one.
		rs := []rune(term)
		titled := string(unicode.ToUpper(rs[0])) + string(rs[1:])
		match = "(instr(c.raw_text, ?) > 0 OR instr(c.raw_text, ?) > 0)"
		matchArgs = []any{term, titled}
	} else {
		match = "instr(lower(c.raw_text), ?) > 0"
		matchArgs = []any{term}
	}

	where := "\n\t\tWHERE " + match
	args := append([]any{}, matchArgs...)
	if len(repos) > 0 {
		where += " AND f.repo IN (" + placeholders(len(repos)) + ")"
		args = append(args, toAny(repos)...)
	}
	stageQ, stageArgs := stage.clause("f")
	where += stageQ
	args = append(args, stageArgs...)

	// The hub guard, before the rows are fetched: a term in more than
	// substringHubShare of the corpus is not evidence, and counting is cheaper
	// than materialising its hits.
	//
	// Numerator and denominator carry the SAME filters. Counting the matches
	// against every chunk in the database would compute the share against a
	// population the turn cannot see: a term in 100% of the one repo in scope
	// is 0.8% of a corpus with a large parked repository in it, and the guard
	// would wave it through. One big parked or out-of-scope repository would
	// otherwise disarm the guard for every live one — the same mistake the
	// vec lane's `rowid IN (…)` rule exists to stop.
	scoped := `
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1`
	scopeWhere := ""
	scopeArgs := []any{}
	if len(repos) > 0 {
		scopeWhere += " WHERE f.repo IN (" + placeholders(len(repos)) + ")"
		scopeArgs = append(scopeArgs, toAny(repos)...)
	}
	stageOnly, stageOnlyArgs := stage.clause("f")
	if stageOnly != "" {
		if scopeWhere == "" {
			// stage.clause emits a leading " AND"; it needs a WHERE to hang on.
			scopeWhere = " WHERE 1=1"
		}
		scopeWhere += stageOnly
		scopeArgs = append(scopeArgs, stageOnlyArgs...)
	}

	//nolint:gosec // only fixed SQL structure is interpolated; every value is a bound ? parameter
	countQ := `SELECT (SELECT count(*)` + scoped + where + `), (SELECT count(*)` + scoped + scopeWhere + `)`
	var matched, total int
	countArgs := append(append([]any{}, args...), scopeArgs...)
	if err := s.db.QueryRowContext(ctx, countQ, countArgs...).Scan(&matched, &total); err != nil {
		return nil, fmt.Errorf("substring search: %w", err)
	}
	if matched == 0 {
		return nil, nil
	}
	if total >= substringHubFloor && float64(matched)/float64(total) > substringHubShare {
		return nil, nil
	}

	// No LIMIT here, and the sort is in Go: address order alone ranks a
	// repository's LAYOUT rather than its relevance, and cutting by it in SQL
	// decides the answer by directory name. In the corpus that motivated the
	// rung the converter sat at position 35 of 40, behind .puml entity
	// diagrams and persistence fixtures that merely name the field.
	//
	// Taking every match first is safe because the hub guard above has already
	// bounded the set: a term reaching more than substringHubShare of the
	// corpus never gets here.
	//
	//nolint:gosec // only fixed SQL structure is interpolated; every value is a bound ? parameter
	q := `SELECT ` + hitColumns + `
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1` + where +
		"\n\t\tORDER BY f.repo, f.path, c.start_line, c.ordinal"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("substring search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.Repo, &h.Branch, &h.Path, &h.Symbol,
			&h.RawText, &h.StartLine, &h.EndLine, &h.Ordinal, &h.SHA); err != nil {
			return nil, fmt.Errorf("substring search: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("substring search: %w", err)
	}

	// Code, then tests, then documentation; address within each. A test proves
	// the mechanism and a diagram names the field, but neither IS the
	// mechanism, and this lane has no bm25 to tell them apart on content.
	//
	// SortStableFunc, and the SQL already ordered by address, so the result is
	// deterministic across runs over one database — what makes a one-part move
	// in a measurement real rather than row order.
	sort.SliceStable(out, func(i, j int) bool {
		return substringKindRank(out[i].Path) < substringKindRank(out[j].Path)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// substringKindRank orders a path by what it IS: code first, then tests, then
// documentation. It decides only this lane's own cut; the fused score is still
// TestDecay's and DocDecay's to set.
func substringKindRank(path string) int {
	switch {
	case IsDocPath(path):
		return 2
	case IsTestPath(path):
		return 1
	default:
		return 0
	}
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
