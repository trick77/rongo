package retrieve

import (
	"strings"
	"unicode"
)

// minSubstringRunes is the shortest candidate the substring rung will scan for.
// It is deliberately well above minPrefixRunes: a prefix at least anchors the
// start of a token, while a substring anchors nothing. "state" as a substring
// is inside half the corpus and says nothing about relevance, whereas
// "anzahlfahrzeuge" is a claim.
const minSubstringRunes = 8

// maxSubstringTerms caps how many candidates one question contributes. Each is
// a separate scan of chunks, so a pasted paragraph must not become forty of
// them.
const maxSubstringTerms = 12

// maxSubstringCodeTerms is how much of that cap the GUESSED identifiers may
// take. The two sources are budgeted apart on purpose: the rung exists for the
// questions where the guess MISSED, and a shared budget lets the misses starve
// the pairs that would have recovered them.
//
// Measured on the question that motivated the rung: the step guessed
// policenantrag, fahrzeuganzahl, weitergabe and datenweitergabe, none of
// them the identifier. Under one shared cap of six those four plus two leading
// pairs filled it, and "anzahlfahrzeuge" — emitted around position nine — was cut.
const maxSubstringCodeTerms = 4

// BuildSubstringTerms derives the identifier-shaped candidates a question is
// worth scanning the corpus for, DETERMINISTICALLY — no model call.
//
// It exists because the keyword lane cannot see an identifier that only ever
// occurs inside a larger token. Java and TypeScript written by German-speaking
// developers reach a field as getAnzahlFahrzeuge and setAnzahlfahrzeuge, and FTS5
// tokenizes each of those whole, so the bare word "anzahlfahrzeuge" matches
// nothing. The prefix rungs cannot help either: they widen to the RIGHT, and
// the word sits at the token's right end.
//
// Two sources, in this order:
//
//	code_terms          already identifier-shaped, taken as given
//	adjacent word pairs the question's own nouns, glued: "Anzahl Fahrzeuge"
//	                    -> anzahlfahrzeuge, "max tokens" -> maxtokens
//
// The pairs are what recovers a question whose code_terms guessed the concept
// instead of the field, or guessed the two nouns in the question's German
// order rather than the code's. Function words are dropped BEFORE pairing, so
// "Anzahl der Fahrzeuge" still yields anzahlfahrzeuge rather than anzahlder.
//
// Order is fixed and duplicates are dropped: the results become lanes, and a
// term fused in twice would double-count its rows against the other lanes —
// the same reason BuildFTSQueries drops its redundant rungs.
func BuildSubstringTerms(question string, codeTerms []string) []string {
	var out []string
	seen := map[string]bool{}

	// add takes a candidate and reports whether there is room for another
	// under `limit`, counting against the SHARED length of out.
	//
	// That shared counter is what makes maxSubstringCodeTerms a CEILING on the
	// code terms rather than a reserved allocation: the guesses may take at
	// most 4 of the 12, and the prose candidates then run against the full 12
	// with whatever the guesses spent already on the clock. It is the right
	// shape here — the point is that a bad guess cannot spend the whole
	// budget, not that prose is entitled to exactly 8 — but a THIRD source
	// added between them would silently share the same counter and get no
	// ceiling of its own. Give one its own limit constant if that day comes.
	add := func(s string, limit int) bool {
		s = fold(s)
		if len([]rune(s)) < minSubstringRunes || seen[s] {
			return len(out) < limit
		}
		seen[s] = true
		out = append(out, s)
		return len(out) < limit
	}

	// addRaw is add for a candidate that is already in the exact spelling the
	// source would use — a snake_case form, whose separators fold() would
	// strip back into the glued shape it exists to differ from.
	addRaw := func(s string, limit int) bool {
		if len([]rune(s)) < minSubstringRunes || seen[s] {
			return len(out) < limit
		}
		seen[s] = true
		out = append(out, s)
		return len(out) < limit
	}

	// Code terms first, under a budget of their own: a guessed identifier is a
	// stronger candidate than a pair glued out of prose, but it is also the
	// thing that missed when the rung is needed at all.
	for _, c := range codeTerms {
		// A guessed term that ALREADY carries separators is kept in that
		// spelling as well as folded. The haystack is raw source, so folding
		// alone destroys a guess that was RIGHT: set_anzahl_fahrzeuge becomes
		// setanzahlfahrzeuge, which cannot occur in a source that writes the
		// underscores. Same for a kebab-case key (max-retry-count) and a
		// dotted name (com.acme.Converter). Without this the rung is dead
		// exactly when the model guessed correctly in a separator language —
		// the opposite of the failure it was built for.
		if c != fold(c) {
			if !addRaw(strings.ToLower(c), maxSubstringCodeTerms) {
				break
			}
		}
		if !add(c, maxSubstringCodeTerms) {
			break
		}
	}

	words := contentWords(question)

	// Adjacent pairs first, then pairs one word apart. The second sweep is what
	// carries a question written in a language `stopwords` does not cover:
	// that list is English by construction, so German "Anzahl der Fahrzeuge" and
	// French "nombre des enfants" keep their article as an ordinary content
	// word and would otherwise only ever yield anzahlder and derfahrzeuge.
	//
	// Adjacency stays the stronger signal and is emitted first, so the budget
	// spends itself on pairs the question actually wrote side by side.
	for gap := 1; gap <= 2; gap++ {
		for i := 0; i+gap < len(words); i++ {
			if !add(words[i]+words[i+gap], maxSubstringTerms) {
				return out
			}
		}
	}

	// Single content words LAST. A question that is one identifier
	// ("CanChoose", and every identifier-kind question in the eval corpus) has
	// no pair to form, and without this the rung never runs on exactly the
	// questions written to measure it. They come after the pairs because a
	// lone word is the weaker claim: "vertragsnehmer" is half the corpus, while
	// "anzahlfahrzeuge" is a mapping.
	for _, w := range words {
		if !add(w, maxSubstringTerms) {
			return out
		}
	}

	// The snake_case spelling of each pair, so the rung is not silently dead
	// over Python, Rust, C and Ruby. The haystack is raw source: a corpus
	// writing set_anzahl_fahrzeuge contains no run of letters spelling
	// "anzahlfahrzeuge", and no case variant of the glued needle can find it.
	//
	// Appended after everything else, and only while the budget allows: it is
	// a spelling guess, weaker than the forms the question actually used.
	for gap := 1; gap <= 2; gap++ {
		for i := 0; i+gap < len(words); i++ {
			if !addRaw(words[i]+"_"+words[i+gap], maxSubstringTerms) {
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

// fold lowercases and strips everything that is not a letter or digit. It
// normalises the CANDIDATE only: "Anzahl Fahrzeuge", anzahlFahrzeuge and
// getAnzahlFahrzeuge all fold to a needle of the same shape.
//
// The haystack is NOT folded. It is raw source text, matched by SQL instr, so
// the needle has to appear in the code exactly as the code writes it. That is
// what SeparatorVariants is for, and what bounds this rung: it reaches an
// identifier written as one run of letters — camelCase, PascalCase, and the
// separator forms that variant produces — and it does not reach one whose
// letters are interrupted in a way no variant reproduces.
//
// strings.ToLower rather than SQL lower(): SQLite's lower() is ASCII-only, so
// folding the needle in Go and the column in SQL would disagree on any
// non-ASCII letter. Matching is therefore done against the raw column, with the
// case variants generated here instead.
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

// hasNonASCII reports whether s contains a rune SQL lower() will not fold.
// SQLite's lower() is ASCII-only, so a needle carrying an umlaut cannot be
// compared case-insensitively by the database and needs its cases spelled out.
func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return true
		}
	}
	return false
}
