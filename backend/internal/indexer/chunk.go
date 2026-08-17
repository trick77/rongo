package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/trick77/rongo/internal/symbols"
)

// Chunk is one unit of retrieval.
//
// Text and RawText are deliberately different things, and that difference is
// why hybrid search works here. Text is what gets EMBEDDED: it carries the
// breadcrumb and the enclosing symbol chain, because a question asked in
// business language ("wie wird die Teaser-Mail verschickt") shares almost no
// words with a method body. RawText is the source alone, which is what the
// keyword lane indexes and what a citation quotes: that lane has to match the
// literal identifier the user typed.
type Chunk struct {
	Ordinal   int
	StartLine int
	EndLine   int
	Symbol    string
	Text      string
	RawText   string
	// SearchText is what the keyword lane indexes. It equals RawText unless
	// comments were stripped, in which case RawText still holds the untouched
	// source: a citation must quote the real file, never a doctored one.
	SearchText  string
	TokenCount  int
	ContentHash string
}

// ChunkOptions controls chunk sizing, in estimated tokens.
type ChunkOptions struct {
	TargetTokens  int
	MaxTokens     int
	OverlapTokens int
	// StripComments removes whole-line comments from the text that is embedded
	// and full-text indexed, leaving only code in the search lanes.
	//
	// The reason is correctness before recall: a comment goes stale, is wrong,
	// or is missing entirely, and a stale one pulls the vector towards a claim
	// no line of code has to honour. An answer built on it is convincingly
	// wrong, which the spec names as the most expensive failure this product
	// can have. What that principle costs in hit rate is a measured arm in the
	// eval harness, not an assumption.
	//
	// Defaults to false so an existing database does not change meaning under a
	// deployment that never set BACKEND_INDEX_COMMENTS.
	StripComments bool
}

// DefaultChunkOptions targets ~600 tokens with an 800 ceiling and ~75 tokens of
// overlap, the sizing peeq's chunker was tuned to.
func DefaultChunkOptions() ChunkOptions {
	return ChunkOptions{TargetTokens: 600, MaxTokens: 800, OverlapTokens: 75}
}

// estimateTokens approximates the token count of s. Real BPE tokenization would
// need a runtime vocabulary download (tiktoken), which this deployment cannot
// rely on; ~4 characters per token is the standard heuristic and is accurate
// enough to size chunks and the embedding budget.
func estimateTokens(s string) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// structuralKinds are the ctags kinds a chunk may be anchored on. Fields,
// members, constants and imports are excluded deliberately: anchoring on them
// would cut a struct in half at its first field, and they are already covered
// by the region their enclosing definition owns.
var structuralKinds = map[string]bool{
	"func": true, "function": true, "method": true, "procedure": true,
	"subroutine": true, "constructor": true, "class": true, "struct": true,
	"interface": true, "trait": true, "enum": true, "type": true,
	"typedef": true, "union": true, "module": true, "namespace": true,
	"object": true, "singletonMethod": true, "macro": true,
}

// commentPrefixes are the line-comment openers rongo recognises when pulling a
// doc comment into its symbol's chunk. It is deliberately a small, syntax-blind
// set rather than a per-language grammar: a wrong guess costs a comment line
// landing in the neighbouring chunk, which degrades a result, it does not break
// one.
var commentPrefixes = []string{"//", "/*", "*", "#", "--", ";", "%", "'''", `"""`}

// ChunkFile splits body into chunks, anchored on symbols where ctags found any
// and on line windows where it did not.
//
// branch is NOT part of the breadcrumb or the content hash, on purpose: the
// same code on two branches embeds identically and must share its cache entry,
// otherwise adding a second branch entry re-embeds a whole repository. The
// branch belongs to the citation, which the write path stores alongside.
func ChunkFile(repo, branch, path string, body []byte, syms []symbols.Symbol, opts ChunkOptions) []Chunk {
	if opts.TargetTokens <= 0 {
		opts = DefaultChunkOptions()
	}
	if opts.MaxTokens < opts.TargetTokens {
		opts.MaxTokens = opts.TargetTokens
	}
	if opts.OverlapTokens < 0 || opts.OverlapTokens >= opts.TargetTokens {
		opts.OverlapTokens = opts.TargetTokens / 8
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}

	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	regions := symbolRegions(lines, syms)

	var out []Chunk
	for _, r := range regions {
		for _, w := range windows(lines, r.start, r.end, opts) {
			raw := strings.Join(lines[w.start-1:w.end], "\n")
			if strings.TrimSpace(raw) == "" {
				// A region of nothing but blank lines carries no answer and
				// would only dilute every result list.
				continue
			}
			chain := symbolChain(r.sym)
			for _, part := range splitOverlongLine(raw, opts.MaxTokens) {
				searchPart := part
				if opts.StripComments {
					searchPart = stripComments(part)
				}
				text := enrich(repo, path, chain, searchPart)
				out = append(out, Chunk{
					Ordinal:     len(out),
					StartLine:   w.start,
					EndLine:     w.end,
					Symbol:      r.sym.Name,
					Text:        text,
					RawText:     part,
					SearchText:  searchPart,
					TokenCount:  estimateTokens(text),
					ContentHash: contentHash(repo, path, chain, searchPart),
				})
			}
		}
	}
	return out
}

// region is one span of the file owned by at most one symbol. Regions partition
// the file: a gap would drop code out of the index while the file still looks
// indexed.
type region struct {
	start, end int
	sym        symbols.Symbol
}

// symbolRegions partitions the file at symbol boundaries. A file with no usable
// symbol is one region covering everything, which the caller then windows.
func symbolRegions(lines []string, syms []symbols.Symbol) []region {
	anchors := anchorSymbols(syms, len(lines))
	if len(anchors) == 0 {
		return []region{{start: 1, end: len(lines)}}
	}

	// Each anchor's region begins at its doc comment rather than at its own
	// line. Decision 4: the doc comment is where business vocabulary literally
	// appears in source, and RawText is what the keyword lane indexes — so it
	// belongs to the symbol it documents, and to that symbol's content hash.
	starts := make([]int, len(anchors))
	for i, a := range anchors {
		floor := 1
		if i > 0 {
			floor = anchors[i-1].Line + 1
		}
		starts[i] = docStart(lines, a.Line, floor)
	}

	var out []region
	if starts[0] > 1 {
		// Everything before the first symbol: package declaration, imports,
		// licence header. Kept rather than dropped — it is what the cross-repo
		// reference walk reads.
		out = append(out, region{start: 1, end: starts[0] - 1})
	}
	for i, a := range anchors {
		end := len(lines)
		if i+1 < len(anchors) {
			end = starts[i+1] - 1
		}
		if end < starts[i] {
			continue
		}
		out = append(out, region{start: starts[i], end: end, sym: a})
	}
	return out
}

// anchorSymbols picks the symbols a chunk may start on: structural ones that do
// not contain another structural symbol.
//
// The innermost wins because a class is not a unit of retrieval — chunking a
// whole Java class would put three unrelated answers into one result and make
// every citation point at the class rather than the method. Where a symbol has
// no children (a Go struct with only fields), it stays the anchor itself.
func anchorSymbols(syms []symbols.Symbol, lineCount int) []symbols.Symbol {
	var structural []symbols.Symbol
	for _, s := range syms {
		if s.Line <= 0 || s.Line > lineCount || !structuralKinds[s.Kind] {
			continue
		}
		structural = append(structural, s)
	}
	var anchors []symbols.Symbol
	for _, s := range structural {
		if !contains(s, structural) {
			anchors = append(anchors, s)
		}
	}
	// ctags is invoked with --sort=no, so the records already arrive in file
	// order; sorting defensively would hide a parser that stopped doing so.
	for i := 1; i < len(anchors); i++ {
		if anchors[i].Line <= anchors[i-1].Line {
			return nil
		}
	}
	return anchors
}

// contains reports whether s encloses another structural symbol, by line range
// where ctags reported an end line and by scope name otherwise.
func contains(s symbols.Symbol, all []symbols.Symbol) bool {
	for _, o := range all {
		if o.Line == s.Line && o.Name == s.Name {
			continue
		}
		if s.End > 0 {
			// The line range is authoritative wherever ctags reported one. The
			// scope fallback below must NOT also apply then: a Java constructor
			// carries its class's name, so scope matching alone would read
			// every method of the class as nested inside the constructor and
			// drop the constructor's own chunk.
			if o.Line > s.Line && o.Line <= s.End {
				return true
			}
			continue
		}
		// Languages ctags reports no end line for still carry the scope, e.g.
		// scope "AbandonedCartJob" or "shop.cart.AbandonedCartJob".
		if o.Scope != "" && (o.Scope == s.Name || strings.HasSuffix(o.Scope, "."+s.Name)) {
			return true
		}
	}
	return false
}

// docStart walks up from a symbol's line over its contiguous comment block,
// stopping at a blank line, at a non-comment line, or at floor.
func docStart(lines []string, line, floor int) int {
	start := line
	for l := line - 1; l >= floor; l-- {
		t := strings.TrimSpace(lines[l-1])
		if t == "" || !isComment(t) {
			break
		}
		start = l
	}
	return start
}

// stripComments drops whole-line comments, keeping every line that carries
// code.
//
// Deliberately line-based and syntax-blind, like docStart: a trailing comment
// after code stays. Removing it would need a real lexer per language, and a
// naive cut at the first "//" would mangle every URL and every string literal
// containing one — a wrong guess there deletes code, which is far worse than
// leaving a few words of prose behind.
func stripComments(s string) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t != "" && isComment(t) {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

func isComment(trimmed string) bool {
	for _, p := range commentPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// span is one emitted window of lines, inclusive on both ends.
type span struct{ start, end int }

// windows splits an inclusive line range into token-bounded windows that
// overlap, so a symbol split across two chunks keeps its context in both.
//
// A single line longer than the budget is emitted alone rather than cut: a
// half-line is not code, and the ceiling exists to bound a request, not to
// guarantee an exact size.
func windows(lines []string, from, to int, opts ChunkOptions) []span {
	if from > to {
		return nil
	}
	var out []span
	for start := from; start <= to; {
		end := start
		tokens := 0
		for end <= to {
			t := estimateTokens(lines[end-1]) + 1 // +1 for the newline
			if end > start && tokens+t > opts.TargetTokens {
				break
			}
			tokens += t
			end++
		}
		end--
		out = append(out, span{start: start, end: end})
		if end >= to {
			break
		}
		// Step back over roughly OverlapTokens of trailing lines, then always
		// make at least one line of progress so this cannot loop forever.
		back, ov := end, 0
		for back > start && ov < opts.OverlapTokens {
			ov += estimateTokens(lines[back-1]) + 1
			back--
		}
		next := back + 1
		if next <= start {
			next = start + 1
		}
		start = next
	}
	return out
}

// splitOverlongLine cuts a window that is one enormous line into pieces the
// embedding endpoint will accept. Normal windows come back unchanged.
//
// windows() cannot bound a single line: it emits one whole line even when that
// line alone blows the budget, because half a line is not code. A generated or
// minified file that slipped past the selector can hold a single line of tens of
// thousands of tokens, and sending it would fail the request — losing the whole
// FILE, not just that line. Cutting it here keeps every byte in the index and
// costs only a boundary in the middle of a line nobody reads anyway.
func splitOverlongLine(raw string, maxTokens int) []string {
	if maxTokens <= 0 || estimateTokens(raw) <= maxTokens {
		return []string{raw}
	}
	runes := []rune(raw)
	size := maxTokens * 4 // the inverse of estimateTokens
	out := make([]string, 0, len(runes)/size+1)
	for start := 0; start < len(runes); start += size {
		end := min(start+size, len(runes))
		out = append(out, string(runes[start:end]))
	}
	return out
}

// chainPart is one link of the enclosing-symbol chain, kept as its kind and its
// name rather than as rendered text — see contentHash.
type chainPart struct{ kind, name string }

// symbolChain describes where a chunk sits: the enclosing symbol, then the
// symbol itself.
func symbolChain(s symbols.Symbol) []chainPart {
	var parts []chainPart
	if s.Scope != "" {
		parts = append(parts, chainPart{kind: s.ScopeKind, name: s.Scope})
	}
	if s.Name != "" {
		parts = append(parts, chainPart{kind: s.Kind, name: s.Name})
	}
	return parts
}

// enrich builds the text that is actually embedded:
//
//	shop-backend/src/shop/cart/AbandonedCartJob.java
//	class AbandonedCartJob > method run
//	<the source, doc comment included>
//
// Line one gives the vector lane the repository and path vocabulary, which is
// how a question naming a system reaches the right repository at all. Line two
// gives it the enclosing type, which is what a business-language question
// actually resembles. The source follows unchanged.
func enrich(repo, path string, chain []chainPart, raw string) string {
	var b strings.Builder
	b.WriteString(repo)
	b.WriteString("/")
	b.WriteString(path)
	b.WriteString("\n")
	if rendered := renderChain(chain); rendered != "" {
		b.WriteString(rendered)
		b.WriteString("\n")
	}
	b.WriteString(raw)
	return b.String()
}

func renderChain(chain []chainPart) string {
	parts := make([]string, 0, len(chain))
	for _, p := range chain {
		if p.kind == "" {
			parts = append(parts, p.name)
			continue
		}
		parts = append(parts, p.kind+" "+p.name)
	}
	return strings.Join(parts, " > ")
}

// Canonical separators for the hashed form. They are control characters that
// cannot occur in a path, a symbol name or source text, so no combination of
// fields can collide with another by concatenation.
const (
	fieldSep = "\x1d"
	partSep  = "\x1f"
	kindSep  = "\x1e"
)

// contentHash keys the embedding cache. It covers repo, path, the symbol chain
// and the raw source — everything that went into the embedded text — but over
// the chain's SEMANTIC FIELDS, never its rendered form.
//
// Both halves of that are load-bearing:
//
//   - Hashing the rendered breadcrumb would mean any reword of the template
//     above invalidated every cached vector in the corpus.
//   - Hashing the raw source ALONE — which an earlier draft of the plan called
//     for — would let two identical bodies at different paths share one cache
//     entry, and a hit would then pair one chunk with a vector built from the
//     other file's context. Silent, and nothing downstream would notice.
//
// The price is that moved or renamed code re-embeds rather than hitting the
// cache. That is the correct side to lose on: the whole dev corpus embeds for
// about five cents, and the alternative is a wrong vector.
func contentHash(repo, path string, chain []chainPart, raw string) string {
	canon := make([]string, 0, len(chain))
	for _, p := range chain {
		canon = append(canon, p.kind+kindSep+p.name)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		repo, path, strings.Join(canon, partSep), raw,
	}, fieldSep)))
	return hex.EncodeToString(sum[:])
}
