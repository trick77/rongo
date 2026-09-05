package ask

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	// inDiagram says the open fence is a diagram, the one block whose
	// numbers are claims rather than code.
	inDiagram bool
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
			// Nothing in a fence is a marker, with one exception: a diagram
			// fence cites through the src arrays of its nodes, and those
			// numbers are the same claim a marker in prose is, so they are
			// renumbered with it (diagram.tsx draws them as the same chip).
			if end := strings.Index(s[i:], "```"); end >= 0 {
				b.WriteString(r.fenceBody(s[i:i+end], true))
				b.WriteString("```")
				i += end + 3
				r.inFence = false
				r.inDiagram = false
				continue
			}
			if atEnd {
				b.WriteString(r.fenceBody(s[i:], true))
				return b.String(), ""
			}
			// Trailing backticks may be the start of the close. Counted over
			// what is left to decide, never over the whole buffer: the fence
			// that opened at i is made of backticks too.
			cut := len(s) - trailingBackticks(s[i:], 2)
			if r.inDiagram {
				out, rest := r.rewriteSrc(s[i:cut], false)
				b.WriteString(out)
				return b.String(), rest + s[cut:]
			}
			b.WriteString(s[i:cut])
			return b.String(), s[cut:]
		}
		// The earliest of: a fence, an inline span, a marker.
		j := strings.IndexAny(s[i:], "`[")
		if j < 0 {
			b.WriteString(s[i:])
			return b.String(), ""
		}
		b.WriteString(s[i : i+j])
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
				// The info string says whether this is a diagram fence, whose
				// src arrays renumber; held back until the line is whole,
				// because the tag decides how the whole block is read.
				nl := strings.IndexByte(s[i:], '\n')
				line := s[i:]
				if nl >= 0 {
					line = s[i : i+nl]
				}
				// A second "```" on the same line closes it: that is a span,
				// not a block, and reading the rest of the line as an info
				// string would leave the fence open over the whole answer -
				// every marker after it silently uncited.
				if strings.Contains(line[3:], "```") {
					b.WriteString("```")
					i += 3
					r.inFence = true
					r.inDiagram = false
					continue
				}
				if nl < 0 && !atEnd {
					return b.String(), s[i:]
				}
				head := s[i:]
				if nl >= 0 {
					head = s[i : i+nl+1]
				}
				r.inDiagram = infoTag(head) == "diagram"
				b.WriteString(head)
				i += len(head)
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

// A src array of a diagram node, or the start of one. Anchored on the KEY,
// never on the bracket: a node label may be `parts[2]`, and renumbering that
// would mint a citation out of an index expression - the fabrication the
// whole citation path exists to prevent.
//
// The value is read as a CHAIN of bracket groups, because answerCommon tells
// the model that two sources read [6][25], and it applies that inside the
// fence often enough. Written into JSON the chain is not parseable, so the
// browser shows the diagram as the code block it now is; worse, renumbering
// only the first group leaves the second at prompt numbering, which puts a
// wrong source under a chip. Both groups are read and the value is emitted
// as the one array it was meant to be.
var (
	srcGroup    = `\[(?:\d{1,3}(?:\s*,\s*\d{1,3})*)\]`
	srcAtStart  = regexp.MustCompile(`^"src"\s*:\s*(` + srcGroup + `(?:\s*` + srcGroup + `)*)`)
	srcPrefixRe = regexp.MustCompile(`^"(s(r(c("(\s*(:(\s*(\[[\d\s,]*)?)?)?)?)?)?)?)?$`)
	// What may still grow into another group of the chain: nothing yet, or a
	// bracket that has not closed. Held back until it is decided.
	srcMoreRe = regexp.MustCompile(`^\s*(\[[\d\s,]*)?$`)
	// The seam between two groups of a chain, which becomes the comma the
	// array should have carried.
	srcJoinRe = regexp.MustCompile(`\]\s*\[`)
)

// infoTag reads the language of a fence header the way the browser does
// (markdown.tsx fenceRe): the first token of the info string. The two ends
// must agree on what a diagram fence is - one drawing a diagram the other
// left at prompt numbering would put a wrong source under a chip.
func infoTag(head string) string {
	if f := strings.Fields(strings.Trim(head, "`\n")); len(f) > 0 {
		return f[0]
	}
	return ""
}

// fenceBody hands a complete fence body to the renumberer for a diagram, and
// passes anything else through untouched.
func (r *renumberer) fenceBody(body string, atEnd bool) string {
	if !r.inDiagram {
		return body
	}
	out, rest := r.rewriteSrc(body, atEnd)
	return out + rest
}

// rewriteSrc renumbers the src arrays of a diagram fence, holding back a key
// or an array that has not arrived whole.
func (r *renumberer) rewriteSrc(s string, atEnd bool) (out string, rest string) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], `"src"`)
		if j < 0 {
			// No key left, but the tail may be the start of one.
			if !atEnd {
				for k := len(s) - 1; k >= i && k > len(s)-6; k-- {
					if srcPrefixRe.MatchString(s[k:]) {
						b.WriteString(s[i:k])
						return b.String(), s[k:]
					}
				}
			}
			b.WriteString(s[i:])
			return b.String(), ""
		}
		b.WriteString(s[i : i+j])
		i += j
		if m := srcAtStart.FindStringSubmatch(s[i:]); m != nil {
			// A group that ends where the text does may be the first of a
			// chain: decided only once what follows it has arrived.
			if !atEnd && srcMoreRe.MatchString(s[i+len(m[0]):]) {
				return b.String(), s[i:]
			}
			b.WriteString(`"src":` + r.rewriteArray(srcGroups(m[1])))
			i += len(m[0])
			continue
		}
		if !atEnd && srcPrefixRe.MatchString(s[i:]) {
			return b.String(), s[i:]
		}
		b.WriteString(`"src"`)
		i += len(`"src"`)
	}
	return b.String(), ""
}

// srcGroups flattens a chain of bracket groups into the one group they meant,
// so [6][25] renumbers and is written back as the array [6,25]. What comes
// back out is rewriteArray's own array, sorted; only a group carrying an
// invented number keeps the separators it came in with.
func srcGroups(chain string) string {
	return strings.TrimSuffix(strings.TrimPrefix(srcJoinRe.ReplaceAllString(chain, ","), "["), "]")
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

// rewriteArray writes a diagram node's src back sorted, as the one array the
// browser parses. Its chips are the chips of the prose, so they are ordered
// the same way.
func (r *renumberer) rewriteArray(group string) string {
	dense, ok := r.markerRun(group)
	if !ok {
		return r.rewrite(group)
	}
	parts := make([]string, len(dense))
	for i, d := range dense {
		parts[i] = strconv.Itoa(d)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// citations resolves the markers the answer used, in the reader's numbering.
func (r *renumberer) citations(sources []Source) []Citation {
	out := make([]Citation, 0, len(r.order))
	for i, n := range r.order {
		s := sources[n-1]
		out = append(out, Citation{
			Marker: i + 1, Repo: s.Repo, Branch: s.Branch, Path: s.Path,
			StartLine: s.StartLine, EndLine: s.EndLine, SHA: s.SHA,
		})
	}
	return out
}

// trailingBackticks counts the backticks s ends with, up to max.
func trailingBackticks(s string, max int) int {
	n := 0
	for n < max && n < len(s) && s[len(s)-1-n] == '`' {
		n++
	}
	return n
}
