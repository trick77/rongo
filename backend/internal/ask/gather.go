package ask

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"

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
	Text      string
	// Reason is "hit" for something the search returned, or
	// "reference:<symbol>" for something a hop reached.
	Reason string
	// Hop is 0 for a search hit and counts up from there.
	Hop int
}

// maxDefiners is how many files may define a name before following it is
// pointless. Measured on the real corpus: at 4 it drops Close (31 files), err
// (23) and Error (10) while keeping a genuine service method, which is defined
// once or twice.
const maxDefiners = 4

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
			Text: h.RawText, Reason: "hit", Hop: 0,
		})
		spent += estimateTokens(h.RawText)
	}

	frontier := out
	for hop := 1; hop <= g.opts.MaxHops; hop++ {
		var next []Source
		for _, from := range frontier {
			refs, err := g.referenced(ctx, from)
			if err != nil {
				return nil, err
			}
			for _, ref := range refs {
				if seen[ref.ChunkID] {
					continue
				}
				cost := estimateTokens(ref.Text)
				if spent+cost > g.opts.TokenBudget {
					// Budget reached. The walk STOPS rather than trimming what
					// is already gathered, so what the answer cites is always
					// present — and stopping means stopping: continuing would
					// keep querying the rest of the frontier for rows that can
					// never be taken.
					return out, nil
				}
				seen[ref.ChunkID] = true
				ref.Hop = hop
				spent += cost
				out = append(out, ref)
				next = append(next, ref)
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
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
	q := `
WITH selective AS (
    SELECT s.name
    FROM symbols s
    WHERE s.name IN (` + placeholders(len(names)) + `)
    GROUP BY s.name
    HAVING COUNT(DISTINCT s.file_id) <= ?
)
SELECT DISTINCT c.id, f.repo, r.branch, f.path, c.symbol, c.start_line, c.end_line, c.raw_text, s.name,
       (SELECT COUNT(DISTINCT s2.file_id) FROM symbols s2 WHERE s2.name = s.name) AS definers
FROM symbols s
JOIN selective sel ON sel.name = s.name
JOIN files f  ON f.id = s.file_id
JOIN repo_state r ON r.name = f.repo
JOIN chunks c ON c.file_id = f.id AND s.line BETWEEN c.start_line AND c.end_line
WHERE NOT (f.repo = ? AND f.path = ?)
ORDER BY definers ASC, f.path, c.ordinal`

	args := make([]any, 0, len(names)+3)
	for _, n := range names {
		args = append(args, n)
	}
	args = append(args, maxDefiners, from.Repo, from.Path)

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
		if err := rows.Scan(&s.ChunkID, &s.Repo, &s.Branch, &s.Path, &s.Symbol,
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
