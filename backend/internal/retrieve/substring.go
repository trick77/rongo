package retrieve

import (
	"strings"
	"unicode"
)

// minSubstringRunes is the shortest candidate the substring rung will scan for.
// It is deliberately well above minPrefixRunes: a prefix at least anchors the
// start of a token, while a substring anchors nothing. "state" as a substring
// is inside half the corpus and says nothing about relevance, whereas
// "anzahlkinder" is a claim.
const minSubstringRunes = 8

// maxSubstringTerms caps how many candidates one question contributes. Each is
// a separate scan of chunks, so a pasted paragraph must not become forty of
// them.
const maxSubstringTerms = 6

// BuildSubstringTerms derives the identifier-shaped candidates a question is
// worth scanning the corpus for, DETERMINISTICALLY — no model call.
//
// It exists because the keyword lane cannot see an identifier that only ever
// occurs inside a larger token. Java and TypeScript written by German-speaking
// developers reach a field as getAnzahlKinder and setAnzahlkinder, and FTS5
// tokenizes each of those whole, so the bare word "anzahlkinder" matches
// nothing. The prefix rungs cannot help either: they widen to the RIGHT, and
// the word sits at the token's right end.
//
// Two sources, in this order:
//
//	code_terms          already identifier-shaped, taken as given
//	adjacent word pairs the question's own nouns, glued: "Anzahl Kinder"
//	                    -> anzahlkinder, "max tokens" -> maxtokens
//
// The pairs are what recovers a question whose code_terms guessed the concept
// instead of the field, or guessed the two nouns in the question's German
// order rather than the code's. Function words are dropped BEFORE pairing, so
// "Anzahl der Kinder" still yields anzahlkinder rather than anzahlder.
//
// Order is fixed and duplicates are dropped: the results become lanes, and a
// term fused in twice would double-count its rows against the other lanes —
// the same reason BuildFTSQueries drops its redundant rungs.
func BuildSubstringTerms(question string, codeTerms []string) []string {
	var out []string
	seen := map[string]bool{}

	add := func(s string) bool {
		s = fold(s)
		if len([]rune(s)) < minSubstringRunes || seen[s] {
			return len(out) < maxSubstringTerms
		}
		seen[s] = true
		out = append(out, s)
		return len(out) < maxSubstringTerms
	}

	// Code terms first: a guessed identifier is a stronger candidate than a
	// pair glued out of prose, and the cap should spend itself on those.
	for _, c := range codeTerms {
		if !add(c) {
			return out
		}
	}

	words := contentWords(question)
	// Adjacent pairs first, then pairs one word apart. The second sweep is what
	// carries a question written in a language `stopwords` does not cover:
	// that list is English by construction, so German "Anzahl der Kinder" and
	// French "nombre des enfants" keep their article as an ordinary content
	// word and would otherwise only ever yield anzahlder and derkinder.
	//
	// Adjacency stays the stronger signal and is emitted first, so the cap
	// spends itself on pairs the question actually wrote side by side.
	for gap := 1; gap <= 2; gap++ {
		for i := 0; i+gap < len(words); i++ {
			if !add(words[i] + words[i+gap]) {
				return out
			}
		}
	}
	return out
}

// contentWords splits a question into its content words, folded, with function
// words removed. Removal happens here rather than after pairing so that a
// preposition between two nouns does not block the pair they form in code.
func contentWords(q string) []string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = fold(f)
		if f == "" {
			continue
		}
		if _, stop := stopwords[f]; stop {
			continue
		}
		out = append(out, f)
	}
	return out
}

// fold lowercases and strips everything that is not a letter or digit, which is
// how a written identifier becomes the form the scan compares: SetAnzahlKinder,
// set_anzahl_kinder and "Anzahl Kinder" all fold to the same needle.
//
// strings.ToLower rather than SQL lower(): SQLite's lower() is ASCII-only, so
// folding both sides in Go keeps a non-ASCII term from silently comparing
// unfolded against the column.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
