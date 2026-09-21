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

// AccessorPrefixes are the conventional verbs a field's accessor is spelled
// with. Deliberately few: each costs one scan, and a wrong guess finds nothing
// rather than the wrong file.
//
// They exist because the FIELD name is usually not selective while its
// ACCESSOR is. Measured on a Java estate: "anzahlkinder" occurs in 90 files —
// every DTO, form, validation and translation that names the field — while
// getAnzahlKinder occurs in 8 and setAnzahlkinder in 2. The question yields
// the field name, because that is the word a reader writes; the code reaches
// the value through the accessor.
var AccessorPrefixes = []string{"get", "set", "is", "has", "with"}

// BuildAccessorTerms derives the accessor spellings of a question's substring
// terms, DETERMINISTICALLY — no model call, the way BuildSubstringTerms
// already derives the snake_case spelling.
//
// The result is not another lane. A term this selective is not a retrieval
// candidate to be ranked against others; its hits ARE the answer, and they
// belong in the gathered set the way a search hit does. Ranking them is what
// loses them: a chunk only the substring scan can see carries one lane where
// the chunks merely NAMING the field carry three, and fusion rewards
// agreement.
// maxAccessorStems is how many of a question's terms may be prefixed at all.
//
// The gate counts every spelling against the corpus in one pass, but SQLite
// still evaluates one instr() per term per ROW, so the scan's cost is the term
// count whether the queries are batched or not. Measured on a 25k-chunk
// corpus: 53 spellings cost 1260 ms against the substring lane's 443.
//
// The cap is small because the terms are ORDERED and the order is meaningful:
// BuildSubstringTerms emits the guessed identifiers first, then the question's
// adjacent word pairs, then the one-apart pairs. The identifier a reader wrote
// is at the front; "backendwie" is at the back.
const maxAccessorStems = 4

// BuildAccessorTerms derives the accessor spellings of a question's substring
// terms. See the type comment above for why it exists; this is what it costs.
func BuildAccessorTerms(terms []string) []string {
	if len(terms) > maxAccessorStems {
		terms = terms[:maxAccessorStems]
	}
	out := make([]string, 0, len(terms)*len(AccessorPrefixes))
	seen := map[string]bool{}
	for _, t := range terms {
		// A pair glued out of the question's PROSE is not a field name. The
		// substring rung emits them because a German compound really is
		// written as one identifier — anzahlfahrzeuge — but an accessor of
		// "backendwie" or "dieanzahl" occurs in no source ever written, and
		// each one costs a full instr() per row of the corpus.
		//
		// A stem is only worth prefixing if it could BE a field: no function
		// word at either end. The list is the stopwords the keyword lane
		// already keeps, in the languages a question arrives in.
		// A term that is ALREADY an accessor is taken as it is, never prefixed
		// again. getAnzahlKinder is the spelling a caller writes, and
		// "getgetanzahlkinder" occurs in no source ever written — so prefixing
		// it spends five scans to guarantee five misses, and throws away the
		// one candidate most likely to be right. The guessed code terms are
		// where this bites: the understanding step guesses accessors, which is
		// exactly what it should do.
		if p := accessorPrefixOf(t); p != "" {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
			continue
		}
		// A separator spelling gets no glued prefix. BuildSubstringTerms emits
		// anzahl_fahrzeuge for a corpus that writes underscores, and such a
		// corpus writes get_anzahl_fahrzeuge, never getanzahl_fahrzeuge: the
		// candidate cannot match, so it is a scan spent on a certainty.
		if strings.ContainsRune(t, '_') {
			continue
		}
		for _, p := range AccessorPrefixes {
			c := p + t
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// accessorStopwords are the function words that make a glued pair prose
// rather than a field name. Deliberately short and deliberately NOT the
// keyword lane's `stopwords`: that list is English by construction and is
// about what to drop from a MATCH, while this is about what a compound
// identifier never starts or ends with. German is here because the questions
// are, and because the pairing that produces "dieanzahl" is exactly the sweep
// `stopwords` does not cover.
var accessorStopwords = map[string]bool{
	// English
	"the": true, "a": true, "an": true, "of": true, "in": true, "to": true,
	"for": true, "and": true, "or": true, "is": true, "how": true, "what": true,
	// German
	"der": true, "die": true, "das": true, "den": true, "dem": true, "des": true,
	"ein": true, "eine": true, "einen": true, "einem": true, "im": true, "am": true,
	"wie": true, "wird": true, "wer": true, "was": true, "und": true, "oder": true,
	"von": true, "auf": true, "bei": true, "mit": true, "zum": true,
	// French, for the same reason German is here
	"le": true, "la": true, "les": true, "du": true, "au": true,
	"comment": true, "est": true,
}

// AccessorStems filters a question's substring terms down to the ones worth
// prefixing, by rebuilding the pairs from the question's OWN words and keeping
// only those whose halves are both content words.
//
// Reading the glued term cannot do this. The letters of a function word occur
// inside real ones — "anzahlkinder" ends in "der", "anzahlfahrzeuge" begins
// with "an" — so a rule testing the edges of the glued string throws away the
// two identifiers this rung exists to find. That version was written first and
// the tests caught it; this one asks the question the pairing already answered.
//
// A code term is kept whatever it looks like: it is the understanding step's
// guess at an identifier, not a pair glued out of prose.
func AccessorStems(question string, terms []string, codeTerms []string) []string {
	code := map[string]bool{}
	for _, c := range codeTerms {
		code[fold(c)] = true
	}
	// Every pair the question's CONTENT words can form, at the gaps
	// BuildSubstringTerms pairs at. A term in this set was built from two
	// words that both survived `stopwords`, so neither half is an English
	// function word; accessorStopwords then covers the languages that list
	// does not.
	fromContent := map[string]bool{}
	words := contentWords(question)
	for gap := 1; gap <= 2; gap++ {
		for i := 0; i+gap < len(words); i++ {
			a, b := words[i], words[i+gap]
			if accessorStopwords[a] || accessorStopwords[b] {
				continue
			}
			fromContent[a+b] = true
		}
	}
	for _, w := range words {
		if !accessorStopwords[w] {
			fromContent[w] = true
		}
	}

	var out []string
	for _, t := range terms {
		if code[t] || fromContent[t] {
			out = append(out, t)
		}
	}
	return out
}

// accessorPrefixOf reports which conventional verb a term already begins with,
// or empty. Length-guarded: "istanbul" starts with "is" and is a word, not an
// accessor of "tanbul", so a prefix only counts when what follows it could be
// an identifier in its own right.
func accessorPrefixOf(term string) string {
	for _, p := range AccessorPrefixes {
		if len(term) > len(p)+minAccessorStem && strings.HasPrefix(term, p) {
			return p
		}
	}
	return ""
}

// minAccessorStem is how much has to follow a verb before the term reads as an
// accessor rather than as a word that happens to start with one.
const minAccessorStem = 4

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
