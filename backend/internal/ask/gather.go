package ask

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/trick77/rongo/internal/edges"
	"github.com/trick77/rongo/internal/retrieve"
)

// Source is one piece of code the answer may be built on, and the reason it is
// here. The reason is not decoration: it is what lets an answer say "this file
// came in because grant.go calls NewGrant" instead of presenting twenty files
// as equally relevant.
type Source struct {
	ChunkID   int64
	Repo      string
	Branch    string
	Path      string
	Symbol    string
	StartLine int
	EndLine   int
	// SHA is the commit the file was indexed at; see retrieve.Hit.SHA.
	SHA  string
	Text string
	// Reason is "hit" for something the search returned,
	// "reference:<symbol>" for something a symbol hop reached, or
	// "edge:<kind> <value> from <repo>/<path>" for something reached across a
	// repository boundary on a shared queue name or route.
	Reason string
	// Hop is 0 for a search hit and counts up from there.
	Hop int
}

// maxDefiners is how many files may define a name before following it is
// pointless. The value and its measurement live in internal/edges, which
// walks the same rule; see edges.MaxDefiners.
const maxDefiners = edges.MaxDefiners

// GatherOptions bounds the walk. Both bounds exist because a mechanism spread
// over a handler, a service and a template is exactly what plain top-k misses —
// and following references without a cap walks the whole corpus instead.
type GatherOptions struct {
	// MaxHops is how far the reference walk may travel from a search hit.
	MaxHops int
	// TokenBudget caps the assembled context, in estimated tokens. Search hits
	// are never evicted by it: an answer cites what it was built on, and a
	// citation into dropped material is one rongo cannot stand behind.
	TokenBudget int
	// NoCrossings switches the repository crossing off, leaving the symbol
	// walk alone. The product never sets it; it exists for the evaluation
	// harness, which has to measure the walk with and without the edge table
	// on the same questions to say what the crossing buys.
	NoCrossings bool
	// RouteSuffix lets a crossing match a client's route tail against the
	// path a server serves; see edges.Match. Off until the flow corpus says
	// what it buys.
	RouteSuffix bool
	// WholeFileTokens is the size up to which the file a hit sits in is read
	// whole, in estimated tokens; a larger file contributes the rest of the
	// hit's own symbol only. Zero is off, which the eval's baseline arm uses.
	// See wholeFile.
	WholeFileTokens int
}

// Gatherer expands search hits into the material an answer is written from.
type Gatherer struct {
	db   *sql.DB
	opts GatherOptions
}

// NewGatherer builds a Gatherer.
func NewGatherer(db *sql.DB, o GatherOptions) *Gatherer {
	if o.MaxHops < 0 {
		o.MaxHops = 0
	}
	if o.TokenBudget <= 0 {
		o.TokenBudget = 24000
	}
	return &Gatherer{db: db, opts: o}
}

// Gather turns search hits into sources, following symbol references outward.
//
// No hits means no sources and no error. "No hit means no hit" is an answer the
// caller reports with the terms it tried; inventing a starting point here would
// produce a confident answer about whatever happened to be nearby.
func (g *Gatherer) Gather(ctx context.Context, hits []retrieve.Hit) ([]Source, error) {
	if len(hits) == 0 {
		return nil, nil
	}

	var out []Source
	seen := map[int64]bool{}
	spent := 0

	for _, h := range hits {
		if seen[h.ChunkID] {
			continue
		}
		seen[h.ChunkID] = true
		out = append(out, Source{
			ChunkID: h.ChunkID, Repo: h.Repo, Branch: h.Branch, Path: h.Path,
			Symbol: h.Symbol, StartLine: h.StartLine, EndLine: h.EndLine,
			SHA: h.SHA, Text: h.RawText, Reason: "hit", Hop: 0,
		})
		spent += estimateTokens(h.RawText)
	}

	// take admits one reached chunk under a budget. It reports false when
	// that budget is spent, and the caller then STOPS rather than trimming
	// what is already gathered, so what the answer cites is always present —
	// and stopping means stopping: continuing would keep querying the rest of
	// the frontier for rows that can never be taken.
	take := func(s Source, hop, budget int) bool {
		if seen[s.ChunkID] {
			return true
		}
		cost := estimateTokens(s.Text)
		if spent+cost > budget {
			return false
		}
		seen[s.ChunkID] = true
		s.Hop = hop
		spent += cost
		out = append(out, s)
		return true
	}

	// The symbol walk spends up to the budget less the crossing reserve; see
	// crossingReserve for why the reserve exists and what it costs.
	symbolBudget := g.opts.TokenBudget
	if !g.opts.NoCrossings {
		symbolBudget -= g.opts.TokenBudget / crossingReserve
	}

	// The rest of the file a hit sits in, before any symbol hop: the nearest
	// explanation of a chunk is the chunk beside it, and the two unique
	// questions the walk never reached were a constant explained one chunk
	// away from the hit with no symbol linking them. Under the symbol walk's
	// budget, and taken at hop 0 — it is the hit's own file — but never
	// evicting a hit, which take guarantees.
	if g.opts.WholeFileTokens > 0 {
		filed := map[string]bool{}
		for _, h := range hits {
			key := h.Repo + "\x00" + h.Path
			if filed[key] {
				continue
			}
			filed[key] = true
			more, err := g.wholeFile(ctx, h)
			if err != nil {
				return nil, err
			}
			for _, s := range more {
				if !take(s, 0, symbolBudget) {
					break
				}
			}
		}
	}
	frontier := out
symbols:
	for hop := 1; hop <= g.opts.MaxHops; hop++ {
		var next []Source
		for _, from := range frontier {
			refs, err := g.referenced(ctx, from)
			if err != nil {
				return nil, err
			}
			for _, ref := range mechanismFirst(refs) {
				if seen[ref.ChunkID] {
					continue
				}
				if !take(ref, hop, symbolBudget) {
					break symbols
				}
				next = append(next, ref)
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}
	if g.opts.NoCrossings {
		return out, nil
	}

	// The crossing, from EVERYTHING the walk gathered — hits and references
	// alike. Measured on the flow corpus, every edge-only miss had the same
	// shape: the literal sat in a file NEXT TO the one the answer needs.
	// OrdersController calls config.getPaymentUri(), and the string
	// "/paymentAuth" lives in the properties class the symbol walk reached at
	// hop one. Crossing only from the hits would consult the edge table only
	// for files that happen to carry their own literal.
	//
	// A crossing lands on one chunk and then takes ONE symbol hop on the far
	// side, because the far end of a queue is a Spring configuration class and
	// the file that does the work is the handler it wires up, which carries
	// no literal at all. That is the composed walk docs/measurements/
	// 2026-09-10-integration-edges.md measured: in-repo hop, crossing, in-repo
	// hop. It does not count against MaxHops — a crossing is bounded by the
	// spread ceiling and lands on a single chunk, where a symbol hop fans out
	// — and it runs under the FULL budget, which is what the reserve is for.
	crossed := map[string]bool{}
	starts := append([]Source{}, out...)
	for _, from := range starts {
		key := from.Repo + "\x00" + from.Path
		if crossed[key] {
			// One file crosses once, whichever of its chunks got here.
			continue
		}
		crossed[key] = true
		far, err := g.crossings(ctx, from)
		if err != nil {
			return nil, err
		}
		for _, landing := range mechanismFirst(far) {
			if seen[landing.ChunkID] {
				continue
			}
			if !take(landing, from.Hop+1, g.opts.TokenBudget) {
				return out, nil
			}
			inland, err := g.referenced(ctx, landing)
			if err != nil {
				return nil, err
			}
			for _, ref := range mechanismFirst(inland) {
				if !take(ref, from.Hop+2, g.opts.TokenBudget) {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

// crossingReserve is the share of the token budget the symbol walk leaves
// untouched for repository crossings: one part in six, 4000 of the default
// 24000 tokens.
//
// Without it the crossing never runs on a corpus that fans out. Measured on
// the flow corpus (Java, Spring): twenty hits reached 141 sources at the FIRST
// symbol hop and the budget was gone before a single edge was consulted, so
// the product gathered exactly what the walk alone gathered — 20 of 30 parts,
// and the order-placement flow at 2 of 6 with payment, shipping and the queue
// consumer never in front of the model. A crossing is the one hop the symbol
// walk cannot make, and it is scarce: at most three repositories per token
// and one chunk per landing. Six chunks of room is what the flagship flow
// needs, and a corpus with no edges at all pays for the reserve with six
// fewer reference chunks out of a hundred and ten. What that costs on the Go
// corpus is measured, not assumed: TestEvalMeasureGathered, in
// internal/retrieve/eval.
const crossingReserve = 6

// wholeFile returns the chunks of a hit's file the answer should read
// beside the hit: every other chunk when the file is small (at most
// WholeFileTokens in all), otherwise only the chunks that continue the
// hit's own symbol — a function cut into windows is one mechanism, and
// half of it is not. Ordered by ordinal, so the file reads in order.
func (g *Gatherer) wholeFile(ctx context.Context, h retrieve.Hit) ([]Source, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT c.id, f.repo, r.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text
		FROM files f
		JOIN repo_state r ON r.name = f.repo
		JOIN chunks c ON c.file_id = f.id
		WHERE f.repo = ? AND f.path = ?
		ORDER BY c.ordinal`, h.Repo, h.Path)
	if err != nil {
		return nil, fmt.Errorf("read the file of %s/%s: %w", h.Repo, h.Path, err)
	}
	defer rows.Close()
	var all []Source
	total := 0
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ChunkID, &s.Repo, &s.Branch, &s.Path, &s.SHA, &s.Symbol, &s.StartLine, &s.EndLine, &s.Text); err != nil {
			return nil, fmt.Errorf("scan a chunk of %s/%s: %w", h.Repo, h.Path, err)
		}
		total += estimateTokens(s.Text)
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Source
	for _, s := range all {
		if s.ChunkID == h.ChunkID {
			continue
		}
		switch {
		case total <= g.opts.WholeFileTokens:
			s.Reason = "file:whole"
		case h.Symbol != "" && s.Symbol == h.Symbol:
			s.Reason = "file:symbol"
		default:
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// mechanismFirst orders one hop's candidates so that test files come after
// everything else. A test that references the service is a correct hop and a
// poor waypoint — the composed-walk measurement reached payment/service.go
// through component_test.go — and retrieval already demotes tests for the
// same reason (retrieve.DefaultTestDecay). Within each half the incoming
// order is kept, so the walk stays deterministic.
func mechanismFirst(ss []Source) []Source {
	out := make([]Source, 0, len(ss))
	for _, s := range ss {
		if !retrieve.IsTestPath(s.Path) {
			out = append(out, s)
		}
	}
	for _, s := range ss {
		if retrieve.IsTestPath(s.Path) {
			out = append(out, s)
		}
	}
	return out
}

// crossings finds, in OTHER repositories, the chunk holding the same queue
// name or route literal as from's file — the one link a producer and its
// consumer share when they share no import, no type and no symbol.
//
// It lands on the chunk at the token's line, never on the whole file: the far
// side of "/paymentAuth" is one handler registration in a transport file,
// and the rest of that file is no more relevant than any other. The
// selectivity rule (a token in more than three repositories is a convention,
// not a link) and the enabled-only filter live in edges.Neighbours, so this
// walk and the measurement in internal/edges cannot disagree about which
// tokens cross.
func (g *Gatherer) crossings(ctx context.Context, from Source) ([]Source, error) {
	ns, err := edges.NeighboursWith(ctx, g.db, from.Repo, from.Path, edges.Match{Suffix: g.opts.RouteSuffix})
	if err != nil {
		return nil, fmt.Errorf("cross from %s/%s: %w", from.Repo, from.Path, err)
	}
	var out []Source
	for _, n := range ns {
		var s Source
		err := g.db.QueryRowContext(ctx, `
			SELECT c.id, f.repo, r.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text
			FROM files f
			JOIN repo_state r ON r.name = f.repo
			JOIN chunks c ON c.file_id = f.id
			WHERE f.repo = ? AND f.path = ? AND ? BETWEEN c.start_line AND c.end_line
			ORDER BY c.ordinal
			LIMIT 1`, n.Repo, n.Path, n.Line).Scan(
			&s.ChunkID, &s.Repo, &s.Branch, &s.Path, &s.SHA, &s.Symbol, &s.StartLine, &s.EndLine, &s.Text)
		if err == sql.ErrNoRows {
			// A token on a line no chunk covers — an overlong line that was
			// split, or a file whose chunks moved since the token was written.
			// Nothing to cite, so nothing to gather.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read the far side of %s %q: %w", n.Kind, n.Value, err)
		}
		s.Reason = fmt.Sprintf("edge:%s %s from %s/%s", n.Kind, n.Value, from.Repo, from.Path)
		out = append(out, s)
	}
	return out, nil
}

// referenced finds chunks defining a symbol that from's code actually mentions.
//
// Both halves are required, and that is the whole point: the symbols table says
// where a name is DEFINED, the source text says whether this code depends on
// it. Following every definition would drag in the corpus; following only the
// text would have nowhere to go.
func (g *Gatherer) referenced(ctx context.Context, from Source) ([]Source, error) {
	names := identifiers(from.Text)
	if len(names) == 0 {
		return nil, nil
	}

	// Names defined all over the corpus are filtered out by how common they
	// are, not by a hand-kept stoplist. Measured on the real index: Close is
	// defined in 31 files, err in 23, Error in 10. Following those pulls in
	// alphabetically-first junk until the budget is gone, and the module the
	// question is about never gets reached. A name that thirty files define
	// says nothing about which one this code depends on.
	// `home` is the second half of "cross a repository boundary only with two
	// reasons". The name-based walk cannot tell peeq's randomToken from loom's
	// — sibling products built from the same template define the same
	// identifiers byte for byte — and the ordering below then handed the
	// answer whichever path sorted first. A reader following that citation
	// lands in the other product while the answer says this one.
	//
	// So a name that the source's OWN repository defines is resolved there and
	// nowhere else. Crossing stays possible, and stays the point: a name this
	// repository does not define at all is genuine composition — peeq calling
	// into go-sqlite3 — and still travels.
	q := `
-- Every count here is over ENABLED repositories only. Filtering just the final
-- join would keep a parked repository out of the result while still letting it
-- shape one: its definitions would count towards maxDefiners, push a name over
-- the threshold, and drop it from selective — costing a LIVE repository a hop
-- it should have made. Parked code influences nothing.
WITH selective AS (
    SELECT s.name
    FROM symbols s
    JOIN files sf ON sf.id = s.file_id
    JOIN repo_state sr ON sr.name = sf.repo AND sr.enabled = 1
    WHERE s.name IN (` + placeholders(len(names)) + `)
    GROUP BY s.name
    HAVING COUNT(DISTINCT s.file_id) <= ?
),
home AS (
    SELECT DISTINCT s.name
    FROM symbols s
    JOIN selective sel ON sel.name = s.name
    JOIN files f ON f.id = s.file_id
    WHERE f.repo = ?
)
SELECT DISTINCT c.id, f.repo, r.branch, f.path, f.sha, c.symbol, c.start_line, c.end_line, c.raw_text, s.name,
       (SELECT COUNT(DISTINCT s2.file_id) FROM symbols s2
          JOIN files f2 ON f2.id = s2.file_id
          JOIN repo_state r2 ON r2.name = f2.repo AND r2.enabled = 1
        WHERE s2.name = s.name) AS definers
FROM symbols s
JOIN selective sel ON sel.name = s.name
JOIN files f  ON f.id = s.file_id
-- enabled = 1: a parked repository is not a hop target. AGENTS.md already
-- requires the target to be indexed before crossing a boundary, and a
-- repository the reader cannot see on the Repos page is not one to pull code
-- out of on rongo's own initiative.
JOIN repo_state r ON r.name = f.repo AND r.enabled = 1
JOIN chunks c ON c.file_id = f.id AND s.line BETWEEN c.start_line AND c.end_line
WHERE NOT (f.repo = ? AND f.path = ?)
  AND (f.repo = ? OR s.name NOT IN (SELECT name FROM home))
ORDER BY definers ASC, f.path, c.ordinal`

	args := make([]any, 0, len(names)+4)
	for _, n := range names {
		args = append(args, n)
	}
	args = append(args, maxDefiners, from.Repo, from.Repo, from.Path, from.Repo)

	rows, err := g.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("follow references from %s: %w", from.Path, err)
	}
	defer rows.Close()

	var out []Source
	for rows.Next() {
		var s Source
		var sym string
		var definers int
		if err := rows.Scan(&s.ChunkID, &s.Repo, &s.Branch, &s.Path, &s.SHA, &s.Symbol,
			&s.StartLine, &s.EndLine, &s.Text, &sym, &definers); err != nil {
			return nil, fmt.Errorf("scan reference: %w", err)
		}
		s.Reason = "reference:" + sym
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read references from %s: %w", from.Path, err)
	}
	return out, nil
}

// identifiers pulls the word-shaped tokens out of source text. Deliberately
// crude — it feeds a lookup against a table of known symbol names, so a wrong
// guess finds nothing rather than fetching the wrong file.
func identifiers(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	uniq := map[string]bool{}
	for _, f := range fields {
		if len(f) > 2 {
			uniq[f] = true
		}
	}
	out := make([]string, 0, len(uniq))
	for f := range uniq {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// estimateTokens is the same ~4-characters-per-token heuristic the chunker
// uses, so a budget here means the same thing it does there.
func estimateTokens(s string) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}
