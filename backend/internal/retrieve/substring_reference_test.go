package retrieve

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// referenceSubstring is the rung as it was first written and measured
// (docs/measurements/2026-09-21-substring-rung.md): one count and one fetch
// per term, two scans each. Kept as the definition of what the rung returns,
// so the one-scan version is held to it hit for hit.
func referenceSubstring(s *Store, ctx context.Context, term string, n int, repos []string, stage StagePrefixes) ([]Hit, error) {
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
