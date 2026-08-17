package retrieve

import (
	"strings"
	"unicode"
)

// maxTerms caps how many tokens one query contributes to a MATCH expression.
// FTS5 has its own expression-depth limits, and a pasted stack trace should be
// truncated rather than rejected.
const maxTerms = 32

// minPrefixRunes is the shortest term the prefix rungs will widen. Below it a
// shared opening says nothing about a shared meaning: "main" reaches maintain,
// "form" reaches formula. The point of those rungs is inflection, not any word
// that starts the same.
const minPrefixRunes = 5

// BuildFTSMatch turns a raw query into a safe FTS5 MATCH expression. Every term
// is lowercased, stripped to letters and digits, and re-quoted, so nothing the
// user types can reach FTS5 as syntax.
//
// Safety here comes from RE-EMISSION, not from escaping: MATCH is an expression
// language, and a syntax error in it surfaces as a failed search rather than as
// no results. The only tokens in the output this function did not build itself
// are the words, and they are all inside quotes.
//
// Returns "" when nothing usable remains, which callers treat as "no keyword
// lane" rather than as an error.
func BuildFTSMatch(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(f)
		if f == "" {
			continue
		}
		terms = append(terms, `"`+f+`"`)
	}
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}
	return strings.Join(terms, " ")
}

// FTSTier is one rung of the ladder: a MATCH expression plus the confidence the
// keyword lane earns by answering at that rung.
//
// The weight travels WITH the expression because the ladder drops redundant
// rungs — a query with no stopwords has no separate content rung, so the OR
// floor is the second entry rather than the fourth. A caller weighing by slice
// position would hand that floor near-full confidence.
type FTSTier struct {
	Match  string
	Weight float64
}

// BuildFTSQueries returns the ladder, strictest rung first:
//
//	every term ANDed        — WeightKeywordStrict
//	content terms ANDed     — function words dropped, WeightKeywordContent
//	content prefixes ANDed  — inflection tolerated, WeightKeywordPrefix
//	content prefixes ORed   — the recall floor, WeightKeywordAny
//
// The strict rung is what finds a chunk that literally contains the identifier
// the user typed. The floor exists so a question phrased entirely in prose does
// not leave the keyword lane silent — but it is weighted below the semantic
// lane, because "shares one word" is weaker evidence than "the embedding put it
// near the question".
//
// Redundant rungs are dropped: a duplicate would be a wasted query AND would
// fuse the same rows in twice, double-counting them against the other lanes.
func BuildFTSQueries(q string) []FTSTier {
	strict := BuildFTSMatch(q)
	if strict == "" {
		return nil
	}
	quoted := strings.Fields(strict)
	content := make([]string, 0, len(quoted))
	for _, t := range quoted {
		if _, stop := stopwords[strings.Trim(t, `"`)]; stop {
			continue
		}
		content = append(content, t)
	}

	tiers := []FTSTier{{Match: strict, Weight: WeightKeywordStrict}}
	// Every term was a function word ("wie funktioniert das"): there is no
	// content query to fall back to, so the strict rung stands alone.
	if len(content) == 0 {
		return tiers
	}
	and := strings.Join(content, " ")
	if and != strict {
		tiers = append(tiers, FTSTier{Match: and, Weight: WeightKeywordContent})
	}

	// The same content terms as prefixes, still ANDed: every word must appear,
	// in some inflection. The index has no stemming — plain fts5(raw_text) with
	// the default tokenizer — so "Migrationen" and "Migration" are unrelated
	// tokens, and a question written in the plural could not otherwise reach a
	// chunk that says the singular.
	prefixed := make([]string, len(content))
	for i, t := range content {
		if len([]rune(strings.Trim(t, `"`))) < minPrefixRunes {
			prefixed[i] = t
			continue
		}
		prefixed[i] = t + " *"
	}
	prefixedAnd := strings.Join(prefixed, " ")
	if prefixedAnd != strict && prefixedAnd != and {
		tiers = append(tiers, FTSTier{Match: prefixedAnd, Weight: WeightKeywordPrefix})
	}
	// ORing a single term reproduces the rung above it exactly.
	if len(content) > 1 {
		tiers = append(tiers, FTSTier{Match: strings.Join(prefixed, " OR "), Weight: WeightKeywordAny})
	}
	return tiers
}
