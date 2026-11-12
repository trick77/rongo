package symbols

import (
	"strings"
	"testing"
)

// demoForm and formBlock are fictional M/TEXT files in the shape real ones
// come in: a template with the usual .DSC head, a nested .IF, a table, an
// include of each kind and a preview text after .EOJ; and a text block with a
// .SWITCH holding a conditional .EOJ, a table opened in both branches of an
// .IF and closed after it, a loop and a text frame.
var (
	demoForm  = readTestdata("Demo_Form.mtxt")
	formBlock = readTestdata("#Form_T1000_de.mtxm")
)

const demoTitle = "Payment reminder for an overdue premium"

// bom is the UTF-8 byte order mark, spelled as bytes: Go refuses one in source.
const bom = "\xef\xbb\xbf"

// The head owns everything before the first block and carries the file stem as
// its name, the definition an include resolves to. Each top-level block is an
// anchor of its own, scoped to the file and its title; the preview after .EOJ
// is one too. Nested blocks stay inside their parent.
func TestExtractMTextAnchorsHeadBlocksAndPreview(t *testing.T) {
	syms, err := ExtractMText("Forms/Demo_Form.mtxt", []byte(demoForm))
	if err != nil {
		t.Fatal(err)
	}
	scope := "Demo_Form: " + demoTitle
	checkSymbols(t, syms, []wantSym{
		{"Demo_Form", "template", demoTitle, "", 1, 8},
		{"$Letter.Recipient.HasPostalAddress = true", "if", scope, "template", 9, 16},
		{"width=100", "table", scope, "template", 17, 23},
		{"Demo_Form", "preview", scope, "template", 26, 28},
	})
}

// A text block: the title is the first prose .DSC line, past the project, path,
// empty and GUI lines, with its label prefix cut. A .DSC line is a comment, so
// ".DSC .END" closes nothing; an .EOJ inside a block ends nothing either.
// A table opened in both branches of an .IF belongs to that .IF: its rows and
// the one .TABLE END after it stay in the .IF's chunk. A text frame has an
// end of its own and is a block; .SECTION is a single line and unknown
// directives are ignored.
func TestExtractMTextReadsATextBlock(t *testing.T) {
	syms, err := ExtractMText("Blocks/#Form_T1000_de.mtxm", []byte(formBlock))
	if err != nil {
		t.Fatal(err)
	}
	scope := "Form_T1000_de: Overdue amount paragraph"
	checkSymbols(t, syms, []wantSym{
		{"Form_T1000_de", "textblock", "Overdue amount paragraph", "", 1, 7},
		{"$Letter.Kind", "switch", scope, "textblock", 8, 14},
		{"*T1000_Total > 0", "if", scope, "textblock", 15, 19},
		{"*i < 3", "while", scope, "textblock", 26, 28},
		{"x=1", "textframe", scope, "textblock", 29, 33},
	})
}

// Windows line endings and a byte order mark change nothing: the lines number
// as the chunker numbers them, and no "\r" reaches a name or the title.
func TestExtractMTextIgnoresCRLFAndBOM(t *testing.T) {
	lf, err := ExtractMText("Forms/Demo_Form.mtxt", []byte(demoForm))
	if err != nil {
		t.Fatal(err)
	}
	crlf := bom + strings.ReplaceAll(demoForm, "\n", "\r\n")
	got, err := ExtractMText("Forms/Demo_Form.mtxt", []byte(crlf))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(lf) {
		t.Fatalf("got %d symbols, want %d", len(got), len(lf))
	}
	for i := range lf {
		if got[i] != lf[i] {
			t.Errorf("symbol %d: got %+v, want %+v", i, got[i], lf[i])
		}
	}
}

// A commented-out directive neither opens nor closes a block, and a commented
// title candidate is code, not prose.
func TestExtractMTextTreatsDSCAsAComment(t *testing.T) {
	body := ".DSC .SET *x,1\n.DSC Real title\n.DSC .IF *x = 1\n.IF *y = 2\n.DSC .END\n.DSC .TABLE END\n.END\n"
	syms, err := ExtractMText("Blocks/#Demo_Block.mtxm", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"Demo_Block", "textblock", "Real title", "", 1, 3},
		{"*y = 2", "if", "Demo_Block: Real title", "textblock", 4, 7},
	})
}

// A file whose first block starts before any other line has an empty head.
// Anchoring both on line 1 would make the chunker drop every anchor of the
// file, so the head takes that block in and runs to the next one.
func TestExtractMTextHeadTakesInABlockOnTheFirstLine(t *testing.T) {
	body := ".IF *HV_GUISPRACHE = \"de\"\n.SET *x,1\n.END\n.DSC Picked after the block\n.IF *y = 2\n.END\n"
	syms, err := ExtractMText("#Demo_Block.mtxm", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"Demo_Block", "textblock", "Picked after the block", "", 1, 4},
		{"*y = 2", "if", "Demo_Block: Picked after the block", "textblock", 5, 6},
	})

	// Blank lines before the block are no head either.
	syms, err = ExtractMText("#Demo_Block.mtxm", []byte("\n\n.WHILE *i < 2\n.END\n"))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{{"Demo_Block", "textblock", "", "", 1, 4}})
}

// Without a title the blocks are scoped to the stem alone. Metadata lines
// (GUI, EXPLAINMODE, a display name, separators) are no title.
func TestExtractMTextWithoutATitle(t *testing.T) {
	body := ".DSC GUI x\n.DSC EXPLAINMODE on\n.DSC DPY Shown name\n.DSC =====\n.DSC TEXT HINT x\n.WITH $Letter\n.END\n"
	syms, err := ExtractMText("Demo_Form.mtxt", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"Demo_Form", "template", "", "", 1, 5},
		{"$Letter", "with", "Demo_Form", "template", 6, 7},
	})
}

// A block that never closes, or an .END with nothing open, is a file the
// reader does not understand. It keeps the head over the whole file, so the
// stem stays a definition, and says nothing about blocks.
func TestExtractMTextKeepsOnlyTheHeadOnABlockError(t *testing.T) {
	for name, body := range map[string]string{
		"unclosed":      ".DSC A title\n.IF *x = 1\n.FOREACH $Letter.Lines\nline\n.END\n",
		"stray end":     ".DSC A title\n.IF *x = 1\n.END\n.END\n",
		"unclosed tbl":  ".DSC A title\n.TABLE\n.ROW\n",
		"preview ender": ".DSC A title\n.EOJ\n.DSC TEXT BEGIN\n.DSC never ends\n.IF *x\n",
	} {
		syms, err := ExtractMText("Demo_Form.mtxt", []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		lines := strings.Count(body, "\n")
		checkSymbols(t, syms, []wantSym{{"Demo_Form", "template", "A title", "", 1, lines}})
	}
}

// A preview that is never closed runs to the end of the file.
func TestExtractMTextOpenPreviewRunsToTheEnd(t *testing.T) {
	body := ".DSC A title\n.EOJ\n.DSC TEXT BEGIN\n.DSC Prose\n"
	syms, err := ExtractMText("Demo_Form.mtxt", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"Demo_Form", "template", "A title", "", 1, 2},
		{"Demo_Form", "preview", "Demo_Form: A title", "template", 3, 4},
	})
}

// Directive names and end markers are matched without regard to case, and a
// name never carries more than a line's worth of condition.
func TestExtractMTextIgnoresCaseAndCapsNames(t *testing.T) {
	long := strings.Repeat("a", 200)
	body := ".dsc A title\n.Table x\n.table End\n.if " + long + "\n.end\n"
	syms, err := ExtractMText("Demo_Form.mtxt", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"Demo_Form", "template", "A title", "", 1, 1},
		{"x", "table", "Demo_Form: A title", "template", 2, 3},
		{strings.Repeat("a", 80), "if", "Demo_Form: A title", "template", 4, 5},
	})
}

// A block without an argument is named by its kind.
func TestExtractMTextNamesABareBlockByItsKind(t *testing.T) {
	syms, err := ExtractMText("Demo_Form.mtxt", []byte(".DSC A title\n.TABLE\n.TABLE END\n"))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{
		{"Demo_Form", "template", "A title", "", 1, 1},
		{"table", "table", "Demo_Form: A title", "template", 2, 3},
	})
}

// The stem drops the "#" a text block's file name starts with, and nothing
// but the M/TEXT extension: a stem may carry dots of its own.
func TestExtractMTextNamesTheFileByItsStem(t *testing.T) {
	syms, err := ExtractMText("Blocks/#done.Created.onclose.mtxm", []byte("text\n"))
	if err != nil {
		t.Fatal(err)
	}
	checkSymbols(t, syms, []wantSym{{"done.Created.onclose", "textblock", "", "", 1, 1}})
}

func TestExtractMTextEmpty(t *testing.T) {
	for _, body := range []string{"", "  \n\t\n", bom, bom + "\r\n"} {
		syms, err := ExtractMText("Demo_Form.mtxt", []byte(body))
		if err != nil || syms != nil {
			t.Errorf("%q: got %v, %v; want nil, nil", body, syms, err)
		}
	}
}

// The chunker's anchor set comes from here, so it cannot drift from what the
// reader emits.
func TestMTextAnchorKinds(t *testing.T) {
	anchors := MTextAnchorKinds()
	for _, k := range strings.Fields("template textblock preview if while foreach switch with table textframe") {
		if !anchors[k] {
			t.Errorf("%s does not anchor", k)
		}
	}
	if len(anchors) != 10 {
		t.Errorf("anchors = %v, want the ten kinds the reader emits", anchors)
	}
	// Each call is a fresh copy: a caller changing its set changes nobody
	// else's.
	anchors["if"] = false
	if !MTextAnchorKinds()["if"] {
		t.Error("the set is shared between callers")
	}
}

// The key an include is resolved by: the stem, case folded beyond ASCII, as
// M/TEXT on Windows compares file names.
func TestMTextStemKey(t *testing.T) {
	for in, want := range map[string]string{
		"Blocks/#Form_Ä1.mtxm":         "form_ä1",
		`Forms\Demo_Form.MTXT`:         "demo_form",
		"done.Created.onclose.mtxm":    "done.created.onclose",
		"Blocks/#Form With Space.mtxm": "form with space",
	} {
		if got := MTextStemKey(in); got != want {
			t.Errorf("MTextStemKey(%q) = %q, want %q", in, got, want)
		}
	}
	if FoldStem("FORM_ä") != FoldStem("form_Ä") {
		t.Error("FoldStem is not case-blind beyond ASCII")
	}
	if MTextLang != "mtext" {
		t.Errorf("MTextLang = %q", MTextLang)
	}
}
