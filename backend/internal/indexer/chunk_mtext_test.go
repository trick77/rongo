package indexer

import (
	"strconv"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/symbols"
)

func mtextChunks(t *testing.T, path string, body []byte) []Chunk {
	t.Helper()
	syms, err := symbols.ExtractMText(path, body)
	if err != nil {
		t.Fatal(err)
	}
	return ChunkFile("letters", path, body, syms, DefaultChunkOptions())
}

func chunkSymbols(chunks []Chunk) []string {
	var out []string
	for _, c := range chunks {
		out = append(out, c.Symbol)
	}
	return out
}

func TestChunkFile_mtextAnchorsOnHeadBlocksAndPreview(t *testing.T) {
	path := "Forms/Demo_Form.mtxt"
	chunks := mtextChunks(t, path, orderContract(t, "Demo_Form.mtxt"))

	want := []string{"Demo_Form", "$Letter.Recipient.HasPostalAddress = true", "width=100", "Demo_Form"}
	if got := chunkSymbols(chunks); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("chunks by symbol:\n got %q\nwant %q", got, want)
	}
	// The head carries the title in its breadcrumb, and keeps it as a line of
	// its own text, which is what the keyword lane reads.
	head := chunks[0]
	if !strings.Contains(head.Text, "Payment reminder for an overdue premium > template Demo_Form\n") {
		t.Errorf("head breadcrumb missing from:\n%s", head.Text)
	}
	if !strings.Contains(head.SearchText, "Payment reminder for an overdue premium") {
		t.Errorf("title missing from the head's search text:\n%s", head.SearchText)
	}
	// A block says which letter it belongs to.
	block := chunks[1]
	if !strings.Contains(block.Text, "template Demo_Form: Payment reminder for an overdue premium > if $Letter") {
		t.Errorf("block breadcrumb missing from:\n%s", block.Text)
	}
	// The preview is a chunk of its own, prose only.
	preview := chunks[3]
	if !strings.HasPrefix(preview.RawText, ".DSC TEXT BEGIN") || preview.EndLine != 28 {
		t.Errorf("preview chunk = lines %d-%d:\n%s", preview.StartLine, preview.EndLine, preview.RawText)
	}
}

// A file whose first line opens a block keeps its anchors: the head takes the
// block in rather than share line 1 with it, which would drop every anchor.
func TestChunkFile_mtextBlockOnTheFirstLineKeepsAnchors(t *testing.T) {
	body := []byte(".IF *HV_GUISPRACHE = \"de\"\n.SET *x,1\n.END\n.DSC A title\n.IF *y = 2\nText\n.END\n")
	chunks := mtextChunks(t, "Blocks/#Demo_Block.mtxm", body)
	want := []string{"Demo_Block", "*y = 2"}
	if got := chunkSymbols(chunks); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("chunks by symbol: got %q, want %q", got, want)
	}
	if chunks[0].StartLine != 1 {
		t.Errorf("head chunk starts at %d, want 1", chunks[0].StartLine)
	}
}

// The M/TEXT kinds anchor only in an M/TEXT file, and an M/TEXT file anchors
// on nothing else.
func TestChunkFile_mtextKindsAnchorOnlyInMText(t *testing.T) {
	sql := []byte("CREATE TABLE orders (\n  id INT\n);\n\nCREATE TABLE lines (\n  id INT\n);\n")
	sqlSyms := []symbols.Symbol{
		{Name: "orders", Kind: "table", Line: 1, End: 3},
		{Name: "lines", Kind: "table", Line: 5, End: 7},
	}
	for _, c := range ChunkFile("shop", "db/schema.sql", sql, sqlSyms, DefaultChunkOptions()) {
		if c.Symbol != "" {
			t.Errorf("a sql file anchored on %q", c.Symbol)
		}
	}

	body := []byte(".DSC A title\nText\n.IF *x\nMore\n.END\n")
	foreign := []symbols.Symbol{{Name: "run", Kind: "function", Line: 2, End: 4}}
	for _, c := range ChunkFile("letters", "Forms/Demo_Form.mtxt", body, foreign, DefaultChunkOptions()) {
		if c.Symbol != "" {
			t.Errorf("an M/TEXT file anchored on the ctags kind of %q", c.Symbol)
		}
	}
}

// chunkSpans renders chunks as "symbol:start-end".
func chunkSpans(chunks []Chunk) string {
	var out []string
	for _, c := range chunks {
		out = append(out, c.Symbol+":"+strconv.Itoa(c.StartLine)+"-"+strconv.Itoa(c.EndLine))
	}
	return strings.Join(out, " ")
}

// In M/TEXT the comment is .DSC: a run of .DSC lines directly above a block
// describes it and joins its chunk. A "#" line is an include and a "*" or
// "%" line output text; neither moves, whatever other languages mean by them.
func TestChunkFile_mtextDSCAboveABlockJoinsIt(t *testing.T) {
	body := []byte(".DSC A title\n.SET *x,1\n" +
		".DSC Greets the customer\n.dsc by name\n.IF *a\n.END\n" + // 3-6
		"#Blocks\\Form_A\n*x\n%Label0001;y\n.IF *b\n.END\n" + // 7-11
		".DSC Not adjacent\n\n.IF *c\n.END\n") // 12-15
	got := chunkSpans(mtextChunks(t, "Forms/Demo_Form.mtxt", body))
	want := "Demo_Form:1-2 *a:3-9 *b:10-13 *c:14-15"
	if got != want {
		t.Errorf("chunks:\n got %s\nwant %s", got, want)
	}
}

// A .DSC run that reaches back to the head is the head's own header, not a
// block's description: the head keeps it, and its chunk still starts on line
// 1 with every anchor in place.
func TestChunkFile_mtextHeaderDSCStaysWithTheHead(t *testing.T) {
	body := []byte(".DSC A title\n.DSC Projekt: Demo_Project\n.DSC ----\n.IF *a\nText\n.END\n")
	got := chunkSpans(mtextChunks(t, "Forms/Demo_Form.mtxt", body))
	if want := "Demo_Form:1-3 *a:4-6"; got != want {
		t.Errorf("chunks:\n got %s\nwant %s", got, want)
	}
}

// Other languages keep the shared comment prefixes.
func TestChunkFile_otherLanguagesKeepTheirDocComments(t *testing.T) {
	body := []byte("package x\n\n# doc\ndef run():\n    pass\n")
	syms := []symbols.Symbol{{Name: "run", Kind: "function", Line: 4, End: 5}}
	got := chunkSpans(ChunkFile("shop", "x.py", body, syms, DefaultChunkOptions()))
	if want := ":1-2 run:3-5"; got != want {
		t.Errorf("chunks:\n got %s\nwant %s", got, want)
	}
}
