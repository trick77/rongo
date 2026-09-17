package ask

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// German is spelled the Swiss way by rewriting the text, not by asking the
// model. The readers are in Switzerland, where ß does not exist, and the
// product's own German strings are already spelled that way (scopeNotice); a
// prompt note asking for the same was measured on 2026-09-16
// (docs/measurements/2026-09-16-swiss-digraphs.md) and moved nothing: the
// gpt-5 series writes "größer" and "fuer" regardless of wording. So the
// spelling is a string function over the prose the model wrote.
//
// Two rules. ß becomes ss, always. ae, oe and ue become the umlaut they stand
// for, unless the word carries the digraph legitimately: a ue after a, e or q
// (neue, Steuer, Quelle) is never a ü, -uell (aktuell, manuell) is the Latin
// suffix, a ue closing the word is English (Value, Continue), and a short
// list covers borrowings and names (Israel, Goethe, Blueprint).
// A compound seam like Konto-erstellung is read wrong by this rule; the eval's
// word list (Answer.Respelled) is where that class shows up.
//
// Nothing shaped like an identifier is touched, with or without backticks:
// camelCase, snake_case, a digit, a path or a dotted name keep the spelling
// the source has, because `pruefeBetrag` renamed is a symbol that does not
// exist. Fenced and inline code never reach here at all - a diagram fence's
// node labels included, which is the one place a reader sees German the
// model spelled: those are JSON strings, and this walks prose.

// speller rewrites prose and remembers what it changed, so the eval can count
// the words the model got wrong even though the reader never sees them.
type speller struct {
	rewrote []string
}

// prose respells every word of s. It is whitespace-aware only: a run of
// non-space characters is one unit, so a path or a dotted name is seen whole
// and left alone, while the punctuation around an ordinary word is copied.
func (sp *speller) prose(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			b.WriteRune(r)
			i += w
			continue
		}
		j := i + w
		for j < len(s) {
			r, w := utf8.DecodeRuneInString(s[j:])
			if unicode.IsSpace(r) {
				break
			}
			j += w
		}
		b.WriteString(sp.run(s[i:j]))
		i = j
	}
	return b.String()
}

// run respells one whitespace-delimited unit: each run of letters in it is a
// word, unless the unit is an identifier, which is copied as it came.
func (sp *speller) run(u string) string {
	if identifierLike(u) {
		return u
	}
	var b strings.Builder
	i := 0
	for i < len(u) {
		r, w := utf8.DecodeRuneInString(u[i:])
		if !unicode.IsLetter(r) {
			b.WriteRune(r)
			i += w
			continue
		}
		j := i + w
		for j < len(u) {
			r, w := utf8.DecodeRuneInString(u[j:])
			if !unicode.IsLetter(r) {
				break
			}
			j += w
		}
		word := u[i:j]
		if got := swissWord(word); got != word {
			sp.rewrote = append(sp.rewrote, word)
			b.WriteString(got)
		} else {
			b.WriteString(word)
		}
		i = j
	}
	return b.String()
}

// identifierLike says the unit is a name from code rather than a word of
// prose: camelCase, an underscore, a digit, a path, a dotted name or a URL.
// Prose never has a lowercase letter followed directly by an uppercase one,
// and never puts a letter on both sides of a dot or a slash.
func identifierLike(u string) bool {
	prev, _ := utf8.DecodeRuneInString(u)
	for i, r := range u {
		switch {
		case r == '_' || r == '@' || unicode.IsDigit(r):
			return true
		case i > 0 && unicode.IsUpper(r) && unicode.IsLower(prev):
			return true
		case i > 0 && (r == '.' || r == '/' || r == ':'):
			next, _ := utf8.DecodeRuneInString(u[i+utf8.RuneLen(r):])
			if unicode.IsLetter(prev) && unicode.IsLetter(next) {
				return true
			}
		}
		prev = r
	}
	return false
}

// swissWord is the spelling of one word of prose: ß to ss, and a transliterated
// umlaut back to the umlaut where the word cannot mean the digraph.
func swissWord(w string) string {
	if !strings.ContainsAny(w, "ßẞaouAOU") {
		return w
	}
	out := eszett.Replace(w)
	if allCaps(out) || legitimateDigraph(out) {
		// An acronym is not a German word, and the listed words keep their
		// digraph whole: "Israel" has no ä in it.
		return out
	}
	rs := []rune(out)
	var b strings.Builder
	b.Grow(len(out))
	var prev rune
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if u, ok := umlautOf[r]; ok && i+1 < len(rs) && rs[i+1] == 'e' && !twoVowels(r, prev, rs[i+2:]) {
			b.WriteRune(u)
			prev = u
			i++
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

var eszett = strings.NewReplacer("ß", "ss", "ẞ", "SS")

var umlautOf = map[rune]rune{'a': 'ä', 'o': 'ö', 'u': 'ü', 'A': 'Ä', 'O': 'Ö', 'U': 'Ü'}

// twoVowels says the ue starting with r is u and e rather than a ü. After a,
// e, q or ä it always is: aü, eü, qü and äü do not exist (neue, Steuer,
// Quelle, Bäuerin). At the end of the word, with or without a plural s, it is
// an English word in German prose (Value, Continue, Revenue, Issues): no
// German word ends in ü spelled ue, and Menue is the one loss.
func twoVowels(r, prev rune, after []rune) bool {
	if r != 'u' && r != 'U' {
		return false
	}
	if strings.ContainsRune("aeqäAEQÄ", prev) {
		return true
	}
	return len(after) == 0 || (len(after) == 1 && after[0] == 's')
}

func allCaps(w string) bool {
	n := 0
	for _, r := range w {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			n++
		}
	}
	return n > 1
}

// legitimateDigraph says whether w carries its ae/oe/ue for a reason other
// than a missing umlaut. -uell (aktuell, manuell, virtuell, individuell) is
// the Latin suffix, matched at the word's end with its inflection and told
// apart from the German ü before ll by the letter in front (uellSuffix,
// germanUell); the rest is a short list of borrowings and names, compared
// lowercase by prefix so inflections ride along.
func legitimateDigraph(w string) bool {
	lw := strings.ToLower(w)
	if uellSuffix.MatchString(lw) && !germanUell.MatchString(lw) {
		return true
	}
	for _, s := range legitimateStems {
		if strings.HasPrefix(lw, s) {
			return true
		}
	}
	return false
}

// The Latin suffix follows t, n, s, d or x (aktuell, manuell, visuell,
// graduell, sexuell, Duell); the German ü before ll follows f, h, m, g, br
// or kn (füllen, Hülle, Müller, Gülle, Brüller, Knüller), so those letters
// decide.
var (
	uellSuffix = regexp.MustCompile(`uell(st)?(e[nrsm]?|er[ens]?)?$`)
	germanUell = regexp.MustCompile(`(f|h|m|g|br|kn)uell`)
)

// Names, borrowings and the Latin -uen/-uenz words, by prefix. Only whole
// stems that no German word shares: "due" would keep duenn, "true" would
// keep trueb.
var legitimateStems = []string{
	"aerosol", "aero", "israel", "michael", "rafael", "raphael", "samuel",
	"manuel", "emanuel", "gabriel", "joel", "noel", "goethe", "boeing",
	"phoebe", "poesie", "poet", "koeffizient", "koexist", "aloe", "oboe",
	"kanaen", "bluetooth", "blueprint",
	// Latin: Individuen, Residuen, Statuen, Kongruenz, Duett, Menuett.
	"individu", "residu", "statu", "kongru", "duett", "menuett",
	// English inside German prose, digraph mid-word.
	"guest", "fluent", "influenc", "affluent", "does", "puerto", "suez",
	"cruel", "tuesday",
	// zu-erst, zu-eigen: the prefix zu- before a vowel.
	"zuerst", "zueigen",
}

// A fenced block or an inline span: what swissMarkdown steps over.
var codeSplitRe = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")

// spellFor is the respelling for the fields a German reader sees that are not
// streamed, and the identity for every other language.
func spellFor(lang Language) func(string) string {
	if ParseLanguage(string(lang)) != LanguageDE {
		return func(s string) string { return s }
	}
	return swissMarkdown
}

// swissMarkdown respells a text that was not streamed: a title, a card's
// name, a follow-up. Fenced blocks and inline spans are copied as they came,
// like the renumberer does for the stream.
func swissMarkdown(s string) string {
	sp := &speller{}
	var b strings.Builder
	last := 0
	for _, m := range codeSplitRe.FindAllStringIndex(s, -1) {
		b.WriteString(sp.prose(s[last:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(sp.prose(s[last:]))
	return b.String()
}
