// Package retrieve answers a question against the index by fusing a vec0
// nearest-neighbour lane with an FTS5 keyword lane.
package retrieve

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/trick77/rongo/internal/sqlutil"
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
	q := " AND (" + alias + ".repo NOT IN (" + sqlutil.Placeholders(len(repos)) + ")"
	args = append(args, sqlutil.Args(repos)...)
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
	// The stage restriction rides in the same subquery, for the same reason.
	scopeQ, scopeArgs := repoScope("f2", repos, stage)
	inner += scopeQ
	args = append(args, scopeArgs...)
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

	start := time.Now()
	out, err := s.scanVector(ctx, q, args)
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		// sqlite-vec reports an interrupt as "SQL logic error: chunks iter
		// error", which reads as corruption. Name the cancel and how long the
		// search had run before it.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("vector search interrupted after %s (%w): %w", took, context.Cause(ctx), err)
		}
		return nil, fmt.Errorf("vector search: %w", err)
	}
	if took >= slowVectorSearch {
		slog.Warn("slow vector search", "repos", repos, "k", k, "hits", len(out), "took", took.String())
	}
	return out, nil
}

// slowVectorSearch is the duration past which a vector search is logged. A
// healthy one takes milliseconds; one reading a bloated chunks_vec took
// minutes and showed nothing until the turn died (indexer.CompactVectors).
var slowVectorSearch = 2 * time.Second

func (s *Store) scanVector(ctx context.Context, q string, args []any) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := scanHit(rows, &h, &h.Distance); err != nil {
			return nil, err
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
	scopeQ, scopeArgs := repoScope("f", repos, stage)
	q += scopeQ
	args = append(args, scopeArgs...)
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
		if err := scanHit(rows, &h); err != nil {
			return nil, fmt.Errorf("keyword search: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
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
	laneQ, laneArgs := repoScope("f", repos, stage)
	where += laneQ
	args = append(args, laneArgs...)

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
	// The same repoScope as the numerator's WHERE, hung on a WHERE of its own:
	// the clause opens with " AND", so an always-true predicate carries it.
	scopeWhere, scopeArgs := repoScope("f", repos, stage)
	if scopeWhere != "" {
		scopeWhere = " WHERE 1=1" + scopeWhere
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
		if err := scanHit(rows, &h); err != nil {
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

// SearchSubstringsIn is SearchSubstringIn for every term of a turn at once:
// one list per term, in the order given, each exactly what the single-term
// call returns.
//
// Two scans for all of them. Asked term by term the lane read raw_text twice
// per term — the hub-guard count, then the fetch — which was 24 scans a turn
// (docs/measurements/2026-09-21-substring-rung.md). Here one scan counts
// every term, a second reports which chunks hold the terms the guard let
// through, and only the chunks that survive the cut are read in full
// (docs/measurements/2026-10-02-substring-batched.md).
//
// For ONE term the single-term call is the faster of the two and stays as it
// was; TestSearchSubstringsIn_returnsPerTermWhatThePerTermRungDid holds the
// two to the same result.
func (s *Store) SearchSubstringsIn(ctx context.Context, terms []string, n int, repos []string, stage StagePrefixes) ([][]Hit, error) {
	out := make([][]Hit, len(terms))
	if n <= 0 {
		n = 10
	}

	// The same two match expressions as SearchSubstringIn — what the rung
	// reaches and what it does not is written there — over the subquery's
	// columns instead of the table's.
	type needle struct {
		term  int // index into terms
		match string
		args  []any
	}
	var asked []needle
	folds := false // whether any needle reads the folded text
	for i, term := range terms {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" {
			continue
		}
		if hasNonASCII(term) {
			// Rune-safe: the first character may itself be the multibyte one.
			rs := []rune(term)
			titled := string(unicode.ToUpper(rs[0])) + string(rs[1:])
			asked = append(asked, needle{i, "(instr(s.raw, ?) > 0 OR instr(s.raw, ?) > 0)", []any{term, titled}})
		} else {
			asked = append(asked, needle{i, "instr(s.folded, ?) > 0", []any{term}})
			folds = true
		}
	}
	if len(asked) == 0 {
		return out, nil
	}
	// The match expressions of some needles, comma- or OR-joined, with their
	// arguments in the same order.
	exprs := func(ns []needle, sep string) (string, []any) {
		parts := make([]string, len(ns))
		var args []any
		for i, n := range ns {
			parts[i] = n.match
			args = append(args, n.args...)
		}
		return strings.Join(parts, sep), args
	}

	// The scope every count and every row is taken in. Numerator and
	// denominator of the hub guard carry the SAME filters. Counting the
	// matches against every chunk in the database would compute the share
	// against a population the turn cannot see: a term in 100% of the one repo
	// in scope is 0.8% of a corpus with a large parked repository in it, and
	// the guard would wave it through. One big parked or out-of-scope
	// repository would otherwise disarm the guard for every live one — the
	// same mistake the vec lane's `rowid IN (…)` rule exists to stop.
	const scoped = `
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
		WHERE 1=1`
	scope, scopeArgs := repoScope("f", repos, stage)

	// The fold is the cost, not the scan: lower() copies the chunk, and written
	// into each term's own expression it runs once per term per chunk — one
	// scan built that way measured within a tenth of the 24 it replaced. The
	// inner SELECT computes it once per chunk; LIMIT -1 is what stops SQLite
	// flattening the subquery and putting lower() back into every term.
	//
	// Only when a needle reads it: a search of non-ASCII terms alone matches
	// the raw text, and must not pay for a fold nobody looks at.
	foldCol := ""
	if folds {
		foldCol = ", lower(c.raw_text) AS folded"
	}
	//nolint:gosec // only fixed SQL structure is interpolated; every value is a bound ? parameter
	folded := `
		FROM (SELECT c.id AS id, f.repo AS repo, f.path AS path, c.start_line AS start_line,
			c.ordinal AS ordinal, c.raw_text AS raw` + foldCol + scoped + scope + `
			LIMIT -1) s`

	// One read transaction for the counts, the matches and the rows: under WAL
	// that is one snapshot. Read in separate statements, a poll re-indexing a
	// file in between gave its chunks new ids, and every hit of that file fell
	// out of the lane after the cut had already been made.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("substring search: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// First scan: how many chunks each term is in, and how many there are.
	// The hub guard is decided before a row is fetched — a term in a third of
	// the corpus would otherwise send a third of the corpus through here just
	// to be thrown away.
	counts, countArgs := exprs(asked, "), 0), coalesce(sum(")
	total := 0
	matched := make([]int, len(asked))
	dest := []any{&total}
	for i := range matched {
		dest = append(dest, &matched[i])
	}
	//nolint:gosec // only fixed SQL structure is interpolated; every value is a bound ? parameter
	countQ := `SELECT count(*), coalesce(sum(` + counts + `), 0)` + folded
	if err := tx.QueryRowContext(ctx, countQ, append(countArgs, scopeArgs...)...).Scan(dest...); err != nil {
		return nil, fmt.Errorf("substring search: %w", err)
	}
	var kept []needle
	for i, n := range asked {
		if matched[i] == 0 {
			continue
		}
		// The hub guard: a term in more than substringHubShare of the corpus
		// is not evidence.
		if total >= substringHubFloor && float64(matched[i])/float64(total) > substringHubShare {
			continue
		}
		kept = append(kept, n)
	}
	if len(kept) == 0 {
		return out, nil
	}

	// Second scan: which chunks hold which of the terms that are left. Taking
	// every match is safe because the guard has bounded each term's set.
	//
	// Address order, as the single-term fetch had it: it keeps two runs over
	// one database identical, and the stable kind sort below falls back to it.
	cols, colArgs := exprs(kept, ", ")
	any1, anyArgs := exprs(kept, " OR ")
	//nolint:gosec // only fixed SQL structure is interpolated; every value is a bound ? parameter
	scanQ := `SELECT s.id, s.path, ` + cols + folded + `
		WHERE ` + any1 + `
		ORDER BY s.repo, s.path, s.start_line, s.ordinal`
	args := append(append(colArgs, scopeArgs...), anyArgs...)
	rows, err := tx.QueryContext(ctx, scanQ, args...)
	if err != nil {
		return nil, fmt.Errorf("substring search: %w", err)
	}
	type match struct {
		id   int64
		kind int
	}
	perTerm := make([][]match, len(kept))
	holds := make([]bool, len(kept))
	dest = make([]any, 2+len(kept))
	var id int64
	var path string
	dest[0], dest[1] = &id, &path
	for i := range holds {
		dest[2+i] = &holds[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("substring search: %w", err)
		}
		m := match{id: id, kind: substringKindRank(path)}
		for i, has := range holds {
			if has {
				perTerm[i] = append(perTerm[i], m)
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("substring search: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("substring search: %w", err)
	}

	var want []any
	wanted := map[int64]bool{}
	for i, found := range perTerm {
		// No cut before the sort: address order alone ranks a repository's
		// LAYOUT rather than its relevance, and cutting by it decides the
		// answer by directory name. In the corpus that motivated the rung the
		// converter sat at position 35 of 40, behind .puml entity diagrams and
		// persistence fixtures that merely name the field.
		//
		// Code, then tests, then documentation; address within each. A test
		// proves the mechanism and a diagram names the field, but neither IS
		// the mechanism, and this lane has no bm25 to tell them apart on
		// content. Stable over rows already in address order, so the result is
		// deterministic across runs over one database — what makes a one-part
		// move in a measurement real rather than row order.
		sort.SliceStable(found, func(a, b int) bool { return found[a].kind < found[b].kind })
		if len(found) > n {
			found = found[:n]
		}
		perTerm[i] = found
		for _, m := range found {
			if !wanted[m.id] {
				wanted[m.id] = true
				want = append(want, m.id)
			}
		}
	}
	if len(want) == 0 {
		return out, nil
	}

	// Only what survived the cut is read in full, from the snapshot the ids
	// were read in.
	byID := make(map[int64]Hit, len(want))
	for start := 0; start < len(want); start += substringFetchBatch {
		batch := want[start:min(start+substringFetchBatch, len(want))]
		//nolint:gosec // only fixed SQL structure is interpolated; every value is a bound ? parameter
		q := `SELECT ` + hitColumns + `
			FROM chunks c
			JOIN files f ON f.id = c.file_id
			JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
			WHERE c.id IN (` + sqlutil.Placeholders(len(batch)) + `)`
		if err := readHits(ctx, tx, q, batch, byID); err != nil {
			return nil, err
		}
	}
	for i, found := range perTerm {
		for _, m := range found {
			out[kept[i].term] = append(out[kept[i].term], byID[m.id])
		}
	}
	return out, nil
}

// substringFetchBatch keeps the id list of one read well under SQLite's bound
// on host parameters, whatever the term count and the cut multiply to.
const substringFetchBatch = 500

func readHits(ctx context.Context, tx *sql.Tx, q string, args []any, into map[int64]Hit) error {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("substring search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h Hit
		if err := scanHit(rows, &h); err != nil {
			return fmt.Errorf("substring search: %w", err)
		}
		into[h.ChunkID] = h
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("substring search: %w", err)
	}
	return nil
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

// scanHit reads one hitColumns row into h, plus whatever a lane selects
// after them (the vec lane's distance). One scan for the four lanes, so the
// projection and its reader cannot drift apart.
func scanHit(rows *sql.Rows, h *Hit, extra ...any) error {
	dst := append([]any{&h.ChunkID, &h.Repo, &h.Branch, &h.Path, &h.Symbol,
		&h.RawText, &h.StartLine, &h.EndLine, &h.Ordinal, &h.SHA}, extra...)
	return rows.Scan(dst...)
}

// repoScope is the repository and stage restriction of one lane as a clause
// hanging off an existing WHERE: " AND alias.repo IN (…)" for a named list,
// nothing for the whole corpus, then the stage prefixes. Repo first, stage
// second, in every lane, so the SQL a test captured stays the SQL it compares.
func repoScope(alias string, repos []string, stage StagePrefixes) (string, []any) {
	var q string
	var args []any
	if len(repos) > 0 {
		q = " AND " + alias + ".repo IN (" + sqlutil.Placeholders(len(repos)) + ")"
		args = sqlutil.Args(repos)
	}
	stageQ, stageArgs := stage.clause(alias)
	return q + stageQ, append(args, stageArgs...)
}
