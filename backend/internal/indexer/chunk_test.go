package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/symbols"
)

// javaSource is a three-method class: a constructor and two methods, each with
// its own doc comment. Line numbers are asserted against it, so edits here
// break the boundary tests deliberately.
//
//	 1 package shop.cart;
//	 2
//	 3 import shop.mail.MailSender;
//	 4
//	 5 /** Sends the teaser mail for an abandoned cart. */
//	 6 public class AbandonedCartJob {
//	 7
//	 8     private final MailSender sender;
//	 9
//	10     /** Wires the job to its mail sender. */
//	11     public AbandonedCartJob(MailSender sender) {
//	12         this.sender = sender;
//	13     }
//	14
//	15     /** Runs one pass over the abandoned carts. */
//	16     public void run() {
//	17         sender.send();
//	18     }
//	19
//	20     /** Reports how many carts were seen. */
//	21     public int seen() {
//	22         return 0;
//	23     }
//	24 }
const javaSource = `package shop.cart;

import shop.mail.MailSender;

/** Sends the teaser mail for an abandoned cart. */
public class AbandonedCartJob {

    private final MailSender sender;

    /** Wires the job to its mail sender. */
    public AbandonedCartJob(MailSender sender) {
        this.sender = sender;
    }

    /** Runs one pass over the abandoned carts. */
    public void run() {
        sender.send();
    }

    /** Reports how many carts were seen. */
    public int seen() {
        return 0;
    }
}
`

// javaSymbols is what universal-ctags reports for javaSource. Written as
// literals rather than by shelling out, so this test stays a unit test.
func javaSymbols() []symbols.Symbol {
	return []symbols.Symbol{
		{Name: "shop.cart", Kind: "package", Line: 1, End: 1},
		{Name: "AbandonedCartJob", Kind: "class", Line: 6, End: 24},
		{Name: "sender", Kind: "field", Scope: "AbandonedCartJob", ScopeKind: "class", Line: 8, End: 8},
		{Name: "AbandonedCartJob", Kind: "method", Scope: "AbandonedCartJob", ScopeKind: "class", Line: 11, End: 13},
		{Name: "run", Kind: "method", Scope: "AbandonedCartJob", ScopeKind: "class", Line: 16, End: 18},
		{Name: "seen", Kind: "method", Scope: "AbandonedCartJob", ScopeKind: "class", Line: 21, End: 23},
	}
}

func chunkFor(t *testing.T, chunks []Chunk, symbol string) Chunk {
	t.Helper()
	for _, c := range chunks {
		if c.Symbol == symbol {
			return c
		}
	}
	var got []string
	for _, c := range chunks {
		got = append(got, c.Symbol)
	}
	t.Fatalf("no chunk for symbol %q; got %v", symbol, got)
	return Chunk{}
}

func TestChunkFile_cutsAtSymbolBoundaries(t *testing.T) {
	// Given
	opts := DefaultChunkOptions()

	// When
	chunks := ChunkFile("shop-backend", "master", "src/shop/cart/AbandonedCartJob.java",
		[]byte(javaSource), javaSymbols(), opts)

	// Then: the three methods are the anchors. The class is not — it CONTAINS
	// them, and chunking the whole class would put three unrelated answers in
	// one result.
	run := chunkFor(t, chunks, "run")
	if run.StartLine != 15 || run.EndLine != 19 {
		t.Errorf("run chunk = lines %d-%d, want 15-19 (doc comment through the blank line before the next symbol)",
			run.StartLine, run.EndLine)
	}
	seen := chunkFor(t, chunks, "seen")
	if seen.StartLine != 20 {
		t.Errorf("seen chunk starts at %d, want 20 — its doc comment belongs to it, not to run", seen.StartLine)
	}
	ctor := chunkFor(t, chunks, "AbandonedCartJob")
	if ctor.StartLine != 10 {
		t.Errorf("constructor chunk starts at %d, want 10", ctor.StartLine)
	}
	// Ordinals are dense and ordered, because (file_id, ordinal) is unique.
	for i, c := range chunks {
		if c.Ordinal != i {
			t.Fatalf("chunk %d has ordinal %d, want %d", i, c.Ordinal, i)
		}
	}
}

func TestChunkFile_stripCommentsKeepsTheSourceButNotTheClaim(t *testing.T) {
	// Given: a doc comment carrying business vocabulary the code itself never
	// says. That is precisely what makes it dangerous — it steers the vector
	// towards a claim no line of code has to honour, and it goes stale silently.
	const claim = "abandoned carts"
	on := chunkFor(t, ChunkFile("shop", "master", "src/A.java",
		[]byte(javaSource), javaSymbols(), DefaultChunkOptions()), "run")
	if !strings.Contains(on.Text, claim) {
		t.Fatalf("fixture is useless: the embedded text does not contain %q with comments on", claim)
	}

	// When
	opts := DefaultChunkOptions()
	opts.StripComments = true
	off := chunkFor(t, ChunkFile("shop", "master", "src/A.java",
		[]byte(javaSource), javaSymbols(), opts), "run")

	// Then: gone from what is searched...
	if strings.Contains(off.Text, claim) {
		t.Errorf("embedded text still contains %q", claim)
	}
	if strings.Contains(off.SearchText, claim) {
		t.Errorf("keyword-lane text still contains %q", claim)
	}
	// ...and still there in what a citation quotes, because the source is the
	// source and rongo must never show a doctored file to a reader.
	if !strings.Contains(off.RawText, claim) {
		t.Errorf("RawText lost %q — a citation must quote the real file", claim)
	}
	if !strings.Contains(off.SearchText, "sender.send()") {
		t.Errorf("SearchText lost the code itself: %q", off.SearchText)
	}
	// The embedding cache is keyed by content hash. If stripping did not change
	// it, a re-index would hand back vectors that were computed WITH the
	// comments and the whole arm would measure nothing.
	if off.ContentHash == on.ContentHash {
		t.Error("ContentHash unchanged by stripping — the embedding cache would serve the old vectors")
	}
}

func TestStripComments_neverDeletesCode(t *testing.T) {
	// Stripping is DESTRUCTIVE, so it may not reuse the prefix set docStart
	// works with: that one only decides where a chunk begins, and a wrong guess
	// there costs a comment line. Here a wrong guess deletes code, and the file
	// stops being findable by an identifier that appears only on that line.
	//
	// Each case below is a line that a naive comment-prefix list eats.
	cases := []struct {
		name, path, in string
		want           string
	}{
		{"C dereference", "src/a.go", "*p = v", "*p = v"},
		{"pointer assignment", "src/a.go", "*out = append(*out, x)", "*out = append(*out, x)"},
		{"decrement", "src/a.c", "--n;", "--n;"},
		{"preprocessor", "src/a.c", "#include <stdio.h>", "#include <stdio.h>"},
		{"lisp form", "src/a.el", ";; not stripped where ; is not known to be a comment", ";; not stripped where ; is not known to be a comment"},
		{"modulo line", "src/a.go", "%v formatting", "%v formatting"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripComments(c.path, c.in); got != c.want {
				t.Errorf("stripComments(%q) = %q, want it untouched", c.in, got)
			}
		})
	}
}

func TestStripComments_removesCommentsPerLanguage(t *testing.T) {
	cases := []struct {
		name, path, in, want string
	}{
		{"line comment", "a.go", "// explains\ncode()", "code()"},
		{"block open", "a.go", "/* explains */\ncode()", "code()"},
		{"block continuation", "a.java", "/**\n * explains\n */\ncode()", "code()"},
		{"hash in python", "a.py", "# explains\ncode()", "code()"},
		{"hash in shell", "a.sh", "# explains\ncode()", "code()"},
		// A hash in C is a preprocessor directive, not a comment. Getting this
		// wrong deletes every #include in the corpus.
		{"hash in c stays", "a.c", "#define X 1\ncode()", "#define X 1\ncode()"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripComments(c.path, c.in); got != c.want {
				t.Errorf("stripComments(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestChunkFile_aWindowThatIsOnlyCommentsProducesNoChunk(t *testing.T) {
	// A licence header strips to nothing. Emitting the remainder would store a
	// chunk whose embedded text is the breadcrumb alone — several of them share
	// one content hash, and they dilute every result list. The unstripped path
	// already refuses a blank window for exactly this reason.
	header := "// Copyright 2026\n// All rights reserved.\n// Licensed under X.\n"
	opts := DefaultChunkOptions()
	opts.StripComments = true

	chunks := ChunkFile("shop", "master", "src/LICENSE.go", []byte(header), nil, opts)

	for _, c := range chunks {
		if strings.TrimSpace(c.SearchText) == "" {
			t.Fatalf("chunk %d has empty SearchText; a comment-only window must not be indexed", c.Ordinal)
		}
	}
}

func TestNew_defaultingKeepsAnExplicitStripComments(t *testing.T) {
	// Replacing the whole ChunkOptions struct would silently index comments for
	// a caller who set only StripComments, and the cost of finding out is a
	// full re-embed of the corpus.
	ix := New(Deps{Chunk: ChunkOptions{StripComments: true}})

	if !ix.chunk.StripComments {
		t.Error("StripComments was discarded by defaulting")
	}
	if ix.chunk.TargetTokens != DefaultChunkOptions().TargetTokens {
		t.Errorf("TargetTokens = %d, want the default filled in", ix.chunk.TargetTokens)
	}
}

func TestChunkFile_defaultKeepsComments(t *testing.T) {
	// The switch defaults to keeping comments so an existing database does not
	// change meaning under a deployment that never set the variable.
	if DefaultChunkOptions().StripComments {
		t.Error("DefaultChunkOptions strips comments; the safe default is to keep them")
	}
}

func TestChunkFile_coversEveryLineExactlyOnce(t *testing.T) {
	// Given: symbol regions must partition the file. A gap loses code silently
	// — the answer layer would report "not found" for something that is indexed.
	chunks := ChunkFile("shop-backend", "master", "src/A.java", []byte(javaSource), javaSymbols(), DefaultChunkOptions())

	// Then
	total := strings.Count(javaSource, "\n")
	covered := make([]int, total+2)
	for _, c := range chunks {
		for l := c.StartLine; l <= c.EndLine; l++ {
			covered[l]++
		}
	}
	for l := 1; l <= total; l++ {
		if covered[l] != 1 {
			t.Errorf("line %d covered %d times, want exactly 1", l, covered[l])
		}
	}
}

func TestChunkFile_splitsOversizedSymbolWithOverlap(t *testing.T) {
	// Given: one method far past MaxTokens.
	var b strings.Builder
	b.WriteString("package big;\n")
	b.WriteString("public class Big {\n")
	b.WriteString("    public void huge() {\n")
	for i := 0; i < 400; i++ {
		b.WriteString("        sender.send(\"a fairly long line of code so the token estimate grows\");\n")
	}
	b.WriteString("    }\n}\n")
	syms := []symbols.Symbol{
		{Name: "Big", Kind: "class", Line: 2, End: 404},
		{Name: "huge", Kind: "method", Scope: "Big", ScopeKind: "class", Line: 3, End: 403},
	}

	// When
	chunks := ChunkFile("r", "master", "Big.java", []byte(b.String()), syms, DefaultChunkOptions())

	// Then
	var huge []Chunk
	for _, c := range chunks {
		if c.Symbol == "huge" {
			huge = append(huge, c)
		}
	}
	if len(huge) < 2 {
		t.Fatalf("oversized symbol produced %d chunks, want several", len(huge))
	}
	for i := 1; i < len(huge); i++ {
		if huge[i].StartLine > huge[i-1].EndLine {
			t.Errorf("chunk %d starts at %d after chunk %d ends at %d — the windows must overlap",
				i, huge[i].StartLine, i-1, huge[i-1].EndLine)
		}
	}
	for _, c := range huge {
		if c.TokenCount > DefaultChunkOptions().MaxTokens {
			t.Errorf("chunk of %d tokens exceeds the %d ceiling", c.TokenCount, DefaultChunkOptions().MaxTokens)
		}
	}
}

func TestChunkFile_noSymbolsFallsBackToLineWindows(t *testing.T) {
	// Given: a language ctags knows nothing about. This is the normal path, not
	// a failure path.
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("some prose about the shop and its abandoned carts, line after line\n")
	}
	body := b.String()

	// When
	chunks := ChunkFile("docs", "master", "notes/handbuch.zzz", []byte(body), nil, DefaultChunkOptions())

	// Then
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want several windows", len(chunks))
	}
	if chunks[0].StartLine != 1 {
		t.Errorf("first window starts at %d, want 1", chunks[0].StartLine)
	}
	if last := chunks[len(chunks)-1]; last.EndLine != 500 {
		t.Errorf("last window ends at %d, want 500 — the windows must cover the whole file", last.EndLine)
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].StartLine > chunks[i-1].EndLine {
			t.Errorf("window %d starts at %d after window %d ends at %d — no overlap",
				i, chunks[i].StartLine, i-1, chunks[i-1].EndLine)
		}
	}
}

func TestChunkFile_enrichedTextDiffersFromRawText(t *testing.T) {
	// Given / When
	chunks := ChunkFile("shop-backend", "master", "src/shop/cart/AbandonedCartJob.java",
		[]byte(javaSource), javaSymbols(), DefaultChunkOptions())
	run := chunkFor(t, chunks, "run")

	// Then: the vector lane needs the breadcrumb, because a question in business
	// language shares almost no words with a method body. The keyword lane needs
	// the source alone, because it matches the literal identifier.
	if !strings.HasPrefix(run.Text, "shop-backend/src/shop/cart/AbandonedCartJob.java\n") {
		t.Errorf("Text does not start with the breadcrumb:\n%s", run.Text)
	}
	if !strings.Contains(run.Text, "class AbandonedCartJob > method run") {
		t.Errorf("Text lacks the enclosing symbol chain:\n%s", run.Text)
	}
	if strings.Contains(run.RawText, "shop-backend/src") {
		t.Errorf("RawText carries the breadcrumb, but it must be source only:\n%s", run.RawText)
	}
	if !strings.Contains(run.RawText, "sender.send();") {
		t.Errorf("RawText lacks the body:\n%s", run.RawText)
	}
	// Decision 4: the doc comment is part of RawText, not of the enrichment.
	// It is where business vocabulary literally appears in source, and
	// chunks_fts indexes RawText.
	if !strings.Contains(run.RawText, "Runs one pass over the abandoned carts") {
		t.Errorf("RawText lacks the doc comment:\n%s", run.RawText)
	}
}

func TestChunkFile_hashCoversCanonicalFieldsNotTheRenderedText(t *testing.T) {
	// Given / When
	chunks := ChunkFile("shop-backend", "master", "src/shop/cart/AbandonedCartJob.java",
		[]byte(javaSource), javaSymbols(), DefaultChunkOptions())
	run := chunkFor(t, chunks, "run")

	// Then: the hash is over the breadcrumb's SEMANTIC fields, never its
	// rendered form, so the template above may be reworded without invalidating
	// a single cached vector.
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"shop-backend",
		"src/shop/cart/AbandonedCartJob.java",
		"class\x1eAbandonedCartJob\x1fmethod\x1erun",
		run.RawText,
	}, "\x1d")))
	if want := hex.EncodeToString(sum[:]); run.ContentHash != want {
		t.Errorf("ContentHash = %s, want %s (repo, path, symbol chain and RawText, canonically joined)",
			run.ContentHash, want)
	}
}

func TestChunkFile_hashChangesWithThePath(t *testing.T) {
	// Given: the same body at two paths. The breadcrumb is part of what gets
	// embedded, so the two must NOT share a cache entry — a hit would pair one
	// chunk with a vector built from the other file's context.
	a := ChunkFile("shop-backend", "master", "src/a/AbandonedCartJob.java", []byte(javaSource), javaSymbols(), DefaultChunkOptions())
	b := ChunkFile("shop-backend", "master", "src/b/AbandonedCartJob.java", []byte(javaSource), javaSymbols(), DefaultChunkOptions())

	// Then
	if chunkFor(t, a, "run").ContentHash == chunkFor(t, b, "run").ContentHash {
		t.Error("identical bodies at different paths share a hash; the breadcrumb differs, so the embedding does too")
	}
}

func TestChunkFile_hashIgnoresTheBranch(t *testing.T) {
	// Given: the same code on two branches embeds identically, so it must share
	// its cache entry. Branch belongs to the citation, never to the hash.
	a := ChunkFile("shop-backend", "master", "src/A.java", []byte(javaSource), javaSymbols(), DefaultChunkOptions())
	b := ChunkFile("shop-backend", "release-2024.3", "src/A.java", []byte(javaSource), javaSymbols(), DefaultChunkOptions())

	// Then
	if chunkFor(t, a, "run").ContentHash != chunkFor(t, b, "run").ContentHash {
		t.Error("the branch changed the hash, so a branch entry would re-embed the whole repository")
	}
}

func TestChunkFile_docCommentEditChangesTheHash(t *testing.T) {
	// Given: only the doc comment differs. It is inside RawText, so this is a
	// genuine content change and must miss the cache — otherwise the stored
	// vector would predate the comment it was supposed to include.
	edited := strings.Replace(javaSource,
		"/** Runs one pass over the abandoned carts. */",
		"/** Sendet die Teaser-Mail fuer einen abgebrochenen Warenkorb. */", 1)

	// When
	a := ChunkFile("r", "master", "A.java", []byte(javaSource), javaSymbols(), DefaultChunkOptions())
	b := ChunkFile("r", "master", "A.java", []byte(edited), javaSymbols(), DefaultChunkOptions())

	// Then
	if chunkFor(t, a, "run").ContentHash == chunkFor(t, b, "run").ContentHash {
		t.Error("a doc-comment edit kept the hash; the cached vector would not match the text")
	}
}

func TestChunkFile_cutsASinglePathologicalLine(t *testing.T) {
	// Given: one minified line far past the ceiling. windows() cannot bound it —
	// it emits whole lines — and sending it would fail the embedding request and
	// lose the entire file, not just that line.
	body := "var x = " + strings.Repeat("abcdefghij", 5000) + ";\n"

	// When
	chunks := ChunkFile("ui", "master", "dist/bundle.js", []byte(body), nil, DefaultChunkOptions())

	// Then: every byte is still indexed, in pieces the endpoint will accept.
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want the long line cut into several", len(chunks))
	}
	var rejoined strings.Builder
	for _, c := range chunks {
		if c.TokenCount > 2*DefaultChunkOptions().MaxTokens {
			t.Errorf("chunk of %d tokens is still unbounded", c.TokenCount)
		}
		rejoined.WriteString(c.RawText)
	}
	if rejoined.String() != strings.TrimSuffix(body, "\n") {
		t.Error("the pieces do not rejoin to the original line; code was lost")
	}
}

func TestChunkFile_emptyBodyProducesNoChunks(t *testing.T) {
	// Given / When
	chunks := ChunkFile("r", "master", "empty.go", []byte("\n\n  \n"), nil, DefaultChunkOptions())

	// Then
	if len(chunks) != 0 {
		t.Errorf("got %d chunks for a blank file, want 0", len(chunks))
	}
}
