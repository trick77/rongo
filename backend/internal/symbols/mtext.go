package symbols

import (
	"path"
	"strings"
)

// ExtractMText reads an M/TEXT template (.mtxt) or text block (.mtxm) and
// returns the anchors its chunks start on. ctags has no M/TEXT parser, and the
// language is line-based: a line starting with "." is a directive, one
// starting with "#" includes another file, everything else is output text.
//
// What anchors:
//
//   - The head: line 1 up to the first top-level block, named by the file's
//     stem (the file name without its leading "#" and extension), with the
//     title in its scope. The head's chunk is where an include of this file
//     lands, found by files.stem (MTextStemKey); no M/TEXT anchor is stored
//     as a definition. The title is the first prose .DSC line of the head;
//     the project, path and creation lines, separators and GUI metadata are
//     skipped, and a "%LabelNNNN;" prefix is cut.
//   - Each top-level block: .IF, .WHILE, .FOREACH, .SWITCH and .WITH up to
//     the .END that closes them, and the layout blocks .TABLE and .TEXTFRAME
//     up to their own "END" line. Scoped to the stem and title, so a block's
//     chunk says which letter it belongs to. Nested blocks stay inside.
//   - The preview: ".DSC TEXT BEGIN" to ".DSC TEXT END" after a top-level
//     .EOJ, prose describing the letter in the file's own words.
//
// .DSC is a comment, with no exception: ".DSC .END" closes nothing. Unknown
// directives are ignored. Directive names are matched without regard to case.
//
// Layout blocks are tracked leniently because real files open them
// conditionally: ".IF a / .TABLE x / .ELS / .TABLE y / .END", then the rows,
// then one ".TABLE END". An .END therefore closes its control block together
// with any layout block opened inside it, and a layout end with no matching
// open block is ignored.
//
// A file the block rules cannot read (a block never closed, an .END with
// nothing open) keeps its head over the whole file and no other anchor:
// returning an error would drop the head too, and with it the definition an
// include resolves to. An empty file returns nil.
func ExtractMText(filePath string, body []byte) ([]Symbol, error) {
	lines := MTextLines(body)
	if len(lines) == 0 {
		return nil, nil
	}
	headKind := MTextTextBlockKind
	if strings.EqualFold(path.Ext(filePath), ".mtxt") {
		headKind = MTextTemplateKind
	}
	stem := MTextStem(path.Base(strings.ReplaceAll(filePath, `\`, "/")))

	blocks, ok := mtextBlocks(lines)

	// A block that opens before any non-blank line leaves the head empty. Two
	// anchors on line 1 make the chunker drop every anchor of the file, so the
	// head takes that block in.
	if len(blocks) > 0 && blankBefore(lines, blocks[0].Line) && blocks[0].Kind != mtextPreviewKind {
		blocks = blocks[1:]
	}
	headEnd := len(lines)
	if ok && len(blocks) > 0 {
		headEnd = blocks[0].Line - 1
	}
	titleEnd := headEnd
	if !ok && len(blocks) > 0 {
		// The title is the head's, wherever the reading stopped.
		titleEnd = blocks[0].Line - 1
	}
	title := mtextTitle(lines[:titleEnd])
	head := Symbol{Name: stem, Kind: headKind, Scope: title, Line: 1, End: headEnd}
	if !ok {
		return []Symbol{head}, nil
	}
	scope := stem
	if title != "" {
		scope += ": " + title
	}
	out := []Symbol{head}
	for _, b := range blocks {
		b.Scope, b.ScopeKind = scope, headKind
		if b.Kind == mtextPreviewKind {
			b.Name = stem
		}
		out = append(out, b)
	}
	return out, nil
}

// The head kinds, one per file type: a template is the letter, a text block
// is what a template includes.
const (
	MTextTemplateKind  = "template"
	MTextTextBlockKind = "textblock"
	mtextPreviewKind   = "preview"
)

// mtextControl are the blocks an .END closes; mtextLayout the ones closed by
// their own name followed by END.
var (
	mtextControl = map[string]bool{"IF": true, "WHILE": true, "FOREACH": true, "SWITCH": true, "WITH": true}
	mtextLayout  = map[string]bool{"TABLE": true, "TEXTFRAME": true}
)

// mtextNameRunes caps a block's name, which is its condition: enough to say
// which case the block handles, never a whole line of layout attributes.
const mtextNameRunes = 80

// mtextTitleRunes caps the title in the breadcrumb.
const mtextTitleRunes = 120

// mtextBodyKinds are the kinds of every anchor but the head: blocks, named
// by their condition, and the preview. Listed rather than derived from the
// maps above, whose order is random.
var mtextBodyKinds = []string{"if", "while", "foreach", "switch", "with", "table", "textframe", mtextPreviewKind}

// MTextAnchorKinds returns the kinds ExtractMText emits, which the chunker
// anchors on in an M/TEXT file and nowhere else (some are ctags kinds of
// other languages too: a SQL table). A fresh map per call.
func MTextAnchorKinds() map[string]bool {
	anchors := map[string]bool{MTextTemplateKind: true, MTextTextBlockKind: true}
	for _, k := range mtextBodyKinds {
		anchors[k] = true
	}
	return anchors
}

// MTextLang is files.lang of an M/TEXT template or text block.
const MTextLang = "mtext"

// FoldStem is the case folding an M/TEXT stem is compared under: Unicode,
// not just ASCII, as Windows compares file names. The index stores folded
// stems and include values, so the lookup is an equality an index serves.
func FoldStem(s string) string {
	return strings.ToLower(s)
}

// MTextStemKey is the folded stem of an M/TEXT file path, the key an include
// of it resolves by. Either separator is read.
func MTextStemKey(filePath string) string {
	return FoldStem(MTextStem(path.Base(strings.ReplaceAll(filePath, `\`, "/"))))
}

// MTextLines splits an M/TEXT body the way the chunker numbers it, one
// trailing newline dropped, and trims each line. A byte order mark and the
// "\r" of Windows line endings are removed, or line 1 would not read as
// .DSC and a "\r" would end up in a name. An empty or blank body is nil.
func MTextLines(body []byte) []string {
	s := strings.TrimPrefix(string(body), "\xef\xbb\xbf")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(strings.ReplaceAll(l, "\r", ""))
	}
	return lines
}

// MTextDirective splits a trimmed line into its directive name, upper-cased,
// and the rest. ok is false for a line that is no directive.
func MTextDirective(line string) (name, rest string, ok bool) {
	if !strings.HasPrefix(line, ".") {
		return "", "", false
	}
	i := 1
	for i < len(line) && (isASCIILetter(line[i]) || line[i] == '_') {
		i++
	}
	if i == 1 {
		return "", "", false
	}
	return strings.ToUpper(line[1:i]), strings.TrimSpace(line[i:]), true
}

// IsMTextComment reports a .DSC line: a comment, whatever follows it.
func IsMTextComment(line string) bool {
	name, _, ok := MTextDirective(line)
	return ok && name == "DSC"
}

// MTextStem is the name a file is included by: its base name without the
// M/TEXT extension and without the "#" a text block's name starts with. Only
// the M/TEXT extensions are cut, since a stem may carry dots of its own.
func MTextStem(base string) string {
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".mtxm") || strings.HasSuffix(lower, ".mtxt") {
		base = base[:len(base)-len(".mtxm")]
	}
	return strings.TrimPrefix(base, "#")
}

func isASCIILetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// mtextBlocks returns the top-level blocks and previews in file order, and
// false when the block structure does not close.
func mtextBlocks(lines []string) ([]Symbol, bool) {
	type open struct {
		kind    string
		control bool
	}
	var (
		stack   []open
		out     []Symbol
		eoj     bool
		preview = -1 // index into out of a preview still open
	)
	for i, line := range lines {
		n := i + 1
		name, rest, ok := MTextDirective(line)
		if !ok {
			continue
		}
		if name == "DSC" {
			if len(stack) > 0 || !eoj {
				continue
			}
			words := strings.Fields(strings.ToUpper(rest))
			if len(words) < 2 || words[0] != "TEXT" {
				continue
			}
			switch {
			case words[1] == "BEGIN" && preview < 0:
				out = append(out, Symbol{Kind: mtextPreviewKind, Line: n, End: len(lines)})
				preview = len(out) - 1
			case words[1] == "END" && preview >= 0:
				out[preview].End = n
				preview = -1
			}
			continue
		}
		closing := strings.EqualFold(firstWord(rest), "END")
		switch {
		case mtextControl[name] || (mtextLayout[name] && !closing):
			if len(stack) == 0 {
				blockName := rest
				if blockName == "" {
					blockName = strings.ToLower(name)
				}
				out = append(out, Symbol{Name: truncateRunes(blockName, mtextNameRunes), Kind: strings.ToLower(name), Line: n})
			}
			stack = append(stack, open{kind: name, control: mtextControl[name]})
		case mtextLayout[name]:
			if len(stack) > 0 && stack[len(stack)-1].kind == name {
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					out[len(out)-1].End = n
				}
			}
		case name == "END":
			for len(stack) > 0 && !stack[len(stack)-1].control {
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 {
				return out, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				out[len(out)-1].End = n
			}
		case name == "EOJ" && len(stack) == 0:
			eoj = true
		}
	}
	return out, len(stack) == 0
}

// mtextTitle is the first prose .DSC line among lines, or "".
func mtextTitle(lines []string) string {
	for _, line := range lines {
		name, rest, ok := MTextDirective(line)
		if !ok || name != "DSC" {
			continue
		}
		rest = cutLabel(rest)
		if rest == "" || !hasLetterOrDigit(rest) || strings.ContainsRune(".#*$%", rune(rest[0])) {
			continue
		}
		upper := strings.ToUpper(rest)
		skip := false
		for _, p := range mtextMetaPrefixes {
			if strings.HasPrefix(upper, p) {
				skip = true
				break
			}
		}
		if !skip {
			return truncateRunes(rest, mtextTitleRunes)
		}
	}
	return ""
}

// mtextMetaPrefixes open the .DSC lines that describe the file rather than
// the letter: creation, project and path lines, editor settings, the GUI
// display name and the preview markers.
var mtextMetaPrefixes = []string{
	"ERSTELLT", "PROJEKT", "PFAD", "GUI", "EXPLAINMODE", "DPY", "TEXT BEGIN", "TEXT END", "TEXT HINT",
}

// cutLabel removes a leading "%LabelNNNN;" dialog label, keeping its text.
func cutLabel(s string) string {
	if !strings.HasPrefix(strings.ToLower(s), "%label") {
		return s
	}
	if i := strings.IndexByte(s, ';'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r > 0x7f {
			return true
		}
	}
	return false
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// blankBefore reports whether every line before line n is blank.
func blankBefore(lines []string, n int) bool {
	for _, l := range lines[:n-1] {
		if l != "" {
			return false
		}
	}
	return true
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}
