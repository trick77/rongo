package ask

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// renumberer rewrites citation markers while the answer streams, so the
// reader sees [1], [2], [3] in the order the markers appear rather than the
// index each source had in the prompt. With a hundred sources gathered the
// model cites [107]; that number is the prompt's business, not the reader's.
//
// It sits between the model and BOTH the token stream and the stored text,
// so the record is what the reader watched being written. A marker that
// arrives in pieces ("[", "10", "7]") is held back until it is complete, and
// so is anything else that cannot be decided yet: a partial fence, or the
// rest of a line after a backtick that has not closed. Once decidable, the
// text follows one rule: nothing inside a fenced block or an inline span is
// a marker, because `args[1]` is an index expression, and minting a citation
// for it would put a reference under the answer the model never made. A
// number outside 1..n stays as it came; the UI drops it to plain text.
//
// A run of markers standing together is written back sorted ascending, one
// number per bracket, with a repeat dropped: the reader gets [2][6][7], not
// the order the model happened to reach for its sources in. The numbering
// itself is still first use, so a run is sorted only after its numbers are
// assigned, and the citation list is unaffected.
type renumberer struct {
	n       int         // how many sources the prompt numbered
	dense   map[int]int // the prompt's number -> the reader's
	order   []int       // the reader's number - 1 -> the prompt's
	pending string      // what cannot be decided yet
	inFence bool
	// spell rewrites the prose between the markers and the code, set for a
	// German answer (speller.prose) and nil for every other language. With
	// it set, a word that reaches the end of what has arrived is held back
	// until its end is known: a token boundary falls inside words, and
	// "Gesch" alone cannot be spelled.
	spell func(string) string
}

func newRenumberer(sources int) *renumberer {
	return &renumberer{n: sources, dense: map[int]int{}}
}

// A complete marker at the start of the text, or the start of one. The
// prompt asks for [1][2], but a claim resting on several sources still comes
// out as [1, 2] often enough; read as one marker it matched nothing.
//
// A claim resting on several sources also comes out as a chain of groups,
// [6][2], and a run is sorted as a whole - so the chain is matched as a
// whole. Its seam is spaces and tabs, never \s: a run reaching over a newline
// would swallow the break between the marker that ends one paragraph and the
// one that opens the next.
var (
	markerGroup    = `\[\d{1,3}(?:\s*,\s*\d{1,3})*\]`
	markerGroupRe  = regexp.MustCompile(markerGroup)
	chainAtStart   = regexp.MustCompile(`^` + markerGroup + `(?:[ \t]*` + markerGroup + `)*`)
	markerPrefixRe = regexp.MustCompile(`^\[[\d\s,]*$`)
	// What may still grow into another group of the run: nothing yet, or a
	// bracket that has not closed.
	chainMoreRe = regexp.MustCompile(`^[ \t]*(\[[\d\s,]*)?$`)
	numberRe    = regexp.MustCompile(`\d+`)
)

// feed takes one streamed token and returns what can be emitted so far.
func (r *renumberer) feed(tok string) string {
	r.pending += tok
	out, rest := r.decide(r.pending, false)
	r.pending = rest
	return out
}

// flush ends the stream: whatever is still pending is decided as it stands.
func (r *renumberer) flush() string {
	out, _ := r.decide(r.pending, true)
	r.pending = ""
	return out
}

// decide walks s, rewriting complete markers in prose, and stops at the
// first thing it cannot decide without more text. atEnd says there is no
// more text: an unclosed span is then prose and a half marker is text.
func (r *renumberer) decide(s string, atEnd bool) (out string, rest string) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if r.inFence {
			// Nothing in a fence is a marker. A diagram fence included: its
			// labels are drawn, not cited, and a number in one is a label.
			if end := strings.Index(s[i:], "```"); end >= 0 {
				b.WriteString(s[i : i+end])
				b.WriteString("```")
				i += end + 3
				r.inFence = false
				continue
			}
			if atEnd {
				b.WriteString(s[i:])
				return b.String(), ""
			}
			// Trailing backticks may be the start of the close. Counted over
			// what is left to decide, never over the whole buffer: the fence
			// that opened at i is made of backticks too.
			cut := len(s) - trailingBackticks(s[i:], 2)
			b.WriteString(s[i:cut])
			return b.String(), s[cut:]
		}
		// The earliest of: a fence, an inline span, a marker.
		j := strings.IndexAny(s[i:], "`[")
		if j < 0 {
			if r.spell != nil && !atEnd {
				// The last word may go on in the next token.
				cut := len(s) - trailingWord(s[i:])
				b.WriteString(r.spell(s[i:cut]))
				return b.String(), s[cut:]
			}
			b.WriteString(r.prose(s[i:]))
			return b.String(), ""
		}
		b.WriteString(r.prose(s[i : i+j]))
		i += j
		if s[i] == '`' {
			// One or two backticks at the end of what has arrived may be the
			// start of an opening fence: held back, as the closing one is.
			// Read as an empty inline span they would leave the "```" broken,
			// the block's index expressions read as markers, and the stray
			// third backtick opening a fence that swallows the rest.
			if !atEnd && len(s)-i < 3 && strings.TrimLeft(s[i:], "`") == "" {
				return b.String(), s[i:]
			}
			if strings.HasPrefix(s[i:], "```") {
				// The header line is held back until it is whole: a second
				// "```" on the same line closes it, and that is a span, not
				// a block. Reading the rest of the line as an info string
				// would leave the fence open over the whole answer - every
				// marker after it silently uncited.
				nl := strings.IndexByte(s[i:], '\n')
				line := s[i:]
				if nl >= 0 {
					line = s[i : i+nl]
				}
				if strings.Contains(line[3:], "```") {
					b.WriteString("```")
					i += 3
					r.inFence = true
					continue
				}
				if nl < 0 && !atEnd {
					return b.String(), s[i:]
				}
				raw := s[i:]
				if nl >= 0 {
					raw = s[i : i+nl+1]
				}
				b.WriteString(raw)
				i += len(raw)
				r.inFence = true
				continue
			}
			// An inline span closes on the same line. Until the close or the
			// newline arrives, nothing after the backtick can be decided.
			close := strings.IndexAny(s[i+1:], "`\n")
			switch {
			case close >= 0 && s[i+1+close] == '`':
				b.WriteString(s[i : i+close+2])
				i += close + 2
			case close >= 0 || atEnd:
				// No close on this line: the backtick is text.
				b.WriteByte('`')
				i++
			default:
				return b.String(), s[i:]
			}
			continue
		}
		// A marker, the start of one, or a bracket.
		if m := chainAtStart.FindString(s[i:]); m != "" {
			// A run that ends where the text does is decided only once what
			// follows it has arrived: the next token may bring another group,
			// and the run is sorted as a whole.
			if !atEnd && chainMoreRe.MatchString(s[i+len(m):]) {
				return b.String(), s[i:]
			}
			b.WriteString(r.rewriteChain(m))
			i += len(m)
			continue
		}
		if !atEnd && markerPrefixRe.MatchString(s[i:]) {
			return b.String(), s[i:]
		}
		b.WriteByte('[')
		i++
	}
	return b.String(), ""
}

// DiagramKind returns the type of the diagram an answer carries, as the
// first word of its mermaid fence names it ("flowchart", "sequenceDiagram",
// "stateDiagram-v2", "erDiagram"), and "" when it carries none. It reads the
// text as the renumberer left it. What is measured is the fence, not the
// picture: whether the renderer draws it is the browser's call, and the UI
// corpus test (ui/src/corpus.test.ts) asks the renderer's parser exactly
// that for every answer in the corpus. The answers harness reports it per
// answer.
func DiagramKind(text string) string {
	for rest := text; ; {
		i := strings.Index(rest, "```")
		if i < 0 {
			return ""
		}
		nl := strings.IndexByte(rest[i:], '\n')
		if nl < 0 {
			return ""
		}
		head, after := rest[i:i+nl], rest[i+nl+1:]
		end := strings.Index(after, "\n```")
		if end < 0 {
			return ""
		}
		if tag := infoTag(head); tag == "mermaid" || tag == "diagram" {
			if kind := mermaidKind(after[:end]); kind != "" {
				return kind
			}
		}
		rest = after[end+4:]
	}
}

// mermaidKind is the first word of a mermaid source, past blank lines and
// %% comments or directives, the way the browser reads it (diagram.tsx,
// diagramKind). Empty when there is none, and empty when the word is not
// the shape of a type name: a fence holding the older JSON spec opens with
// a brace, and counting that as a diagram would put a picture in the
// harness's tally that the reader never got.
func mermaidKind(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "%%") {
			continue
		}
		word := strings.TrimRight(strings.Fields(t)[0], ";:")
		if !kindWordRe.MatchString(word) {
			return ""
		}
		return word
	}
	return ""
}

// kindWordRe is the shape of a diagram type name: flowchart, graph,
// sequenceDiagram, stateDiagram-v2, erDiagram.
var kindWordRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// infoTag reads the language of a fence header the way the browser does
// (markdown.tsx fenceRe): the first token of the info string.
func infoTag(head string) string {
	if f := strings.Fields(strings.Trim(head, "`\n")); len(f) > 0 {
		return f[0]
	}
	return ""
}

// rewrite renumbers the numbers of one marker group, keeping its separators.
func (r *renumberer) rewrite(group string) string {
	return "[" + numberRe.ReplaceAllStringFunc(group, func(num string) string {
		n, err := strconv.Atoi(num)
		if err != nil || n < 1 || n > r.n {
			return num // invented: left alone, and never a citation
		}
		return strconv.Itoa(r.denseOf(n))
	}) + "]"
}

// denseOf is the reader's number for a source of the prompt, minted on first
// use: that is what makes an answer count [1], [2], [3] as it is written.
func (r *renumberer) denseOf(n int) int {
	d, ok := r.dense[n]
	if !ok {
		r.order = append(r.order, n)
		d = len(r.order)
		r.dense[n] = d
	}
	return d
}

// markerRun renumbers every number of one run of markers, in the order they
// were written - first use still decides the number, so citations() is
// untouched - and returns them sorted ascending with a repeat dropped. Only
// the reading order changes: [6][2][7] is the same three sources as
// [2][6][7], counted up the way a reader expects them to be.
//
// ok is false when the run carries a number outside 1..n. That one is
// invented and never becomes a citation, so ordering the run would interleave
// real chips with the plain text the UI drops it to; the caller renumbers the
// groups where they stand instead.
func (r *renumberer) markerRun(s string) (dense []int, ok bool) {
	nums := numberRe.FindAllString(s, -1)
	for _, num := range nums {
		if n, err := strconv.Atoi(num); err != nil || n < 1 || n > r.n {
			return nil, false
		}
	}
	seen := make(map[int]bool, len(nums))
	for _, num := range nums {
		n, _ := strconv.Atoi(num)
		d := r.denseOf(n)
		if !seen[d] {
			seen[d] = true
			dense = append(dense, d)
		}
	}
	sort.Ints(dense)
	return dense, true
}

// rewriteChain writes one run of prose markers back sorted, one number per
// bracket - the shape answerCommon asks the model for in the first place.
func (r *renumberer) rewriteChain(chain string) string {
	dense, ok := r.markerRun(chain)
	if !ok {
		return markerGroupRe.ReplaceAllStringFunc(chain, func(g string) string {
			return r.rewrite(strings.Trim(g, "[]"))
		})
	}
	var b strings.Builder
	for _, d := range dense {
		b.WriteByte('[')
		b.WriteString(strconv.Itoa(d))
		b.WriteByte(']')
	}
	return b.String()
}

// citations resolves the markers the answer used, in the reader's numbering.
func (r *renumberer) citations(sources []Source) []Citation {
	out := make([]Citation, 0, len(r.order))
	for i, n := range r.order {
		s := sources[n-1]
		c := Citation{
			Marker: i + 1, Repo: s.Repo, Branch: s.Branch, Path: s.Path,
			StartLine: s.StartLine, EndLine: s.EndLine, SHA: s.SHA,
		}
		if s.IsCommit() {
			c.Kind = SourceCommit
			c.Subject = s.Subject
			c.CommittedAt = s.CommittedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, c)
	}
	return out
}

// prose is the text between markers and code as the reader gets it: spelled
// by r.spell where one is set, as it came otherwise.
func (r *renumberer) prose(s string) string {
	if r.spell == nil {
		return s
	}
	return r.spell(s)
}

// trailingWord is the length in bytes of the run of non-space characters s
// ends with: the word, path or name the next token may still be part of. It
// is counted in whole runes, so what is held back never begins mid-umlaut.
func trailingWord(s string) int {
	n := 0
	for n < len(s) {
		c, w := utf8.DecodeLastRuneInString(s[:len(s)-n])
		if unicode.IsSpace(c) {
			break
		}
		n += w
	}
	return n
}

// trailingBackticks counts the backticks s ends with, up to max.
func trailingBackticks(s string, max int) int {
	n := 0
	for n < max && n < len(s) && s[len(s)-1-n] == '`' {
		n++
	}
	return n
}
