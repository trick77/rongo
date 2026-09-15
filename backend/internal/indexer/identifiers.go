package indexer

import (
	"sort"
	"strings"
	"unicode"
)

// minIdentifierPart is the shortest word an identifier split may contribute.
// One or two letters left over from a boundary ("x" out of getX, "s" out of
// sList) are not vocabulary a question is ever phrased in, and each one costs
// a term in every chunk that holds any identifier at all.
const minIdentifierPart = 2

// splitIdentifiers returns the words hidden inside the identifiers of text,
// lowercased, deduplicated and sorted.
//
// The keyword lane is bare fts5 with the default unicode61 tokenizer: it never
// splits AbandonedCartJob, so a question asking about "abandoned carts" cannot
// reach the class that implements them however literally it names the thing.
// `_`, `.`, `-` and `/` are separators to unicode61 already, so only the case
// and digit boundaries are new here.
//
// Only an identifier that ACTUALLY splits contributes: a single-word name is
// already a token of raw_text, and emitting it again would just weight the
// lane towards long files. Sorted output keeps the stored text a function of
// the chunk alone, so re-indexing unchanged code writes the same bytes.
func splitIdentifiers(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, run := range identifierRuns(text) {
		parts := splitOne(run)
		if len(parts) < 2 {
			// Already one token of raw_text.
			continue
		}
		for _, p := range parts {
			p = strings.ToLower(p)
			if len([]rune(p)) < minIdentifierPart || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// identifierRuns cuts text into maximal runs of identifier characters. Anything
// else — punctuation, whitespace, operators — is a boundary, and so are `.`,
// `-` and `/`, which unicode61 already treats as one.
func identifierRuns(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// splitOne breaks one identifier into its words, on underscores, on the two
// camelCase boundaries and between letters and digits.
//
// The second camel boundary is what keeps an acronym whole: in HTTPServer the
// cut belongs BEFORE the S, not after it, so the words are "HTTP" and "Server"
// rather than "HTTPS" and "erver".
func splitOne(id string) []string {
	// A leading or trailing underscore is a naming convention, not a boundary
	// between words, and left in it would become part of the first word.
	rs := []rune(strings.Trim(id, "_"))
	var parts []string
	start := 0
	flush := func(end int) {
		if end > start {
			parts = append(parts, string(rs[start:end]))
		}
		start = end
	}
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		switch {
		case cur == '_':
			flush(i)
			start = i + 1
		case prev == '_':
			// Already flushed by the branch above.
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			flush(i)
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
			flush(i)
		case unicode.IsDigit(prev) != unicode.IsDigit(cur):
			flush(i)
		}
	}
	flush(len(rs))
	return parts
}
