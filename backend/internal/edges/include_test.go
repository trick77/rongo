package edges

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/symbols"
)

func readMText(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "symbols", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// tokenLines renders include tokens as "value@line", in extraction order.
func tokenLines(toks []Token) []string {
	var out []string
	for _, tok := range toks {
		if tok.Kind != KindInclude {
			continue
		}
		out = append(out, tok.Value+"@"+strconv.Itoa(tok.Line))
	}
	return out
}

// Every place a template names another file: an include line with its
// arguments, across a project and through subfolders; a dialog opened by
// .NEW; a template included by .INC. A language variable is one include
// whose language Included picks, so it is recorded once, with a
// placeholder where the variable stood. Values are case folded, the way
// files.stem is. A .DSC line is a comment and includes nothing.
func TestExtractReadsMTextIncludes(t *testing.T) {
	toks := Extract("Forms/Demo_Form.mtxt", []byte(readMText(t, "Demo_Form.mtxt")))
	want := []string{
		"form_t1000_{lang}@8", "form_d2000@11", "shared_footer@14", "form_attachment@24",
	}
	if got := tokenLines(toks); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("includes:\n got %q\nwant %q", got, want)
	}
	if len(toks) != len(want) {
		t.Errorf("an M/TEXT file yields include tokens only, got %+v", toks)
	}
}

// A stored include target in a .SET value, with the GUI language variable
// spelled in another case, is read too.
func TestExtractReadsAnIncludeInASetValue(t *testing.T) {
	toks := Extract("Blocks/#Form_T1000_de.mtxm", []byte(readMText(t, "#Form_T1000_de.mtxm")))
	want := []string{"form_hint_{lang}@7"}
	if got := tokenLines(toks); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("includes:\n got %q\nwant %q", got, want)
	}
}

func TestExtractMTextIncludeShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"windows endings":        {"\xef\xbb\xbf.DSC t\r\n#Blocks\\Form_A\r\n", []string{"form_a@2"}},
		"extension and hash":     {"#Blocks\\#Form_A.mtxm\n", []string{"form_a@1"}},
		"dotted stem":            {"#Blocks\\done.Created.onclose\n", []string{"done.created.onclose@1"}},
		"forward slashes":        {"#Blocks/Sub/Form_A;x\n", []string{"form_a@1"}},
		"no folder":              {"#Form_A\n", []string{"form_a@1"}},
		"folded beyond ASCII":    {"#Blocks\\FORM_Ä\n", []string{"form_ä@1"}},
		"variable at the end":    {"#Blocks\\Form_*HV_SPRACHE\n", []string{"form_{lang}@1"}},
		"variable mid name":      {"#Blocks\\Form_*HV_SPRACHE-Tab\n", []string{"form_{lang}-tab@1"}},
		"longer variable name":   {"#Blocks\\Form_*HV_SPRACHEKZ\n#Blocks\\Form_*HV_SPRACHE_Tab\n", nil},
		"two variables":          {"#Blocks\\Form_*HV_SPRACHE-*hv_guisprache\n", nil},
		"other variable":         {"#Blocks\\Form_*Kind\n#*Target(1)\n", nil},
		"set without folder":     {".SET *x,#\n.SET *y,#Form_A\n", nil},
		"comment":                {".DSC #Blocks\\Form_A\n.dsc .INC Forms\\Form_B.mtxt\n", nil},
		"other directive":        {".IF *x = #\n.SECTION header=#Blocks\\Form_A\n", nil},
		"one token per line":     {"#Blocks\\Form_A\n#Blocks\\FORM_A\n.SET *a,#Blocks\\Form_A;#Blocks\\Form_A\n", []string{"form_a@1", "form_a@2", "form_a@3"}},
		"inc without target":     {".INC\n", nil},
		"inc with options":       {".INC Forms\\Form_B.mtxt resolve=true\n", []string{"form_b@1"}},
		"inc quoted with spaces": {".INC \"Forms\\My Form.mtxt\" resolve=true\n.INC 'Forms\\Other Form.mtxt'\n", []string{"my form@1", "other form@2"}},
		"several in one .SET":    {".SET *a(1),#Blocks\\Form_A,#Blocks\\Form_B\n", []string{"form_a@1", "form_b@1"}},
		"text line":              {"Number #1 of 3\n", nil},
		"space in a name":        {"#Blocks\\Form With Space ; x\n#Blocks\\Form_B,CLR\n", []string{"form with space@1", "form_b@2"}},
		"quoted .SET":            {".SET *a,\"#Blocks\\Form_A\"\n.SET *b,'#Blocks\\Form B'\n", []string{"form_a@1", "form b@2"}},
		"quoted .NEW":            {".NEW \"#Dialogs\\Form D, x\",CLR,OUT\n", []string{"form d, x@1"}},
		"quoted # line":          {"#\"Blocks\\Form_A\"\n", []string{"form_a@1"}},
		"apostrophe in a name":   {"#Blocks\\O'Neil_Form;$x;y\n.SET *a,#Blocks\\O'Neil_Form,#Blocks\\Form_B\n", []string{"o'neil_form@1", "o'neil_form@2", "form_b@2"}},
	} {
		got := tokenLines(Extract("Forms/Demo_Form.mtxt", []byte(tc.body)))
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

// An include is not an edge: two templates including one shared block say
// nothing about each other. It never crosses and is never held.
func TestNeighboursNeverCrossOnAnInclude(t *testing.T) {
	db := edgeDB(t, "letters", "claims-letters")
	seedFileWithTokens(t, db, "letters", "Forms/A.mtxt", "a", nil,
		[]Token{{Kind: KindInclude, Value: "Shared_Footer", Line: 1}})
	seedFileWithTokens(t, db, "claims-letters", "Forms/B.mtxt", "b", nil,
		[]Token{{Kind: KindInclude, Value: "Shared_Footer", Line: 1}})

	got, err := Neighbours(context.Background(), db, "letters", "Forms/A.mtxt")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	holders, err := Holders(context.Background(), db, KindInclude, "Shared_Footer")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("an include was crossed: %+v", got)
	}
	if len(holders) != 0 {
		t.Errorf("an include was held: %+v", holders)
	}
}

// seedMText inserts an M/TEXT file the way the indexer writes one: lang
// mtext, its folded stem, no symbol, its head chunk and a block chunk, and
// its include tokens, their values folded as Extract folds them.
func seedMText(t *testing.T, db *sql.DB, repo, path, stem string, toks ...Token) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO files (repo, path, sha, lang, skip_reason, stem) VALUES (?, ?, 'deadbeef', ?, '', ?)`,
		repo, path, symbols.MTextLang, symbols.MTextStemKey(path))
	if err != nil {
		t.Fatalf("seed file %s/%s: %v", repo, path, err)
	}
	fileID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	// The head chunk and a block after it: an include lands on the first.
	for i, span := range [][2]int{{1, 10}, {11, 20}} {
		if _, err := db.Exec(`
			INSERT INTO chunks (file_id, ordinal, start_line, end_line, symbol, raw_text, content_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, fileID, i, span[0], span[1], stem, "text of "+stem, repo+path+strconv.Itoa(i)); err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
	}
	for _, tok := range toks {
		if _, err := db.Exec(`INSERT INTO integration_tokens (file_id, kind, value, line) VALUES (?, ?, ?, ?)`,
			fileID, string(tok.Kind), symbols.FoldStem(tok.Value), tok.Line); err != nil {
			t.Fatal(err)
		}
	}
}

func markLibrary(t *testing.T, db *sql.DB, repo string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE repo_state SET library = 1 WHERE name = ?`, repo); err != nil {
		t.Fatal(err)
	}
}

func targets(ls []Landing) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Repo+"/"+l.Path+"@"+strconv.Itoa(l.StartLine)+"-"+strconv.Itoa(l.EndLine)+"="+l.Stem)
	}
	return out
}

// An include resolves to the file with that stem in the template's own
// repository first, and only a stem the repository does not have is looked
// up in a library. Only the includes on the asked lines are followed.
func TestIncludedResolvesHomeBeforeALibrary(t *testing.T) {
	db := edgeDB(t, "letters", "shared-lib")
	markLibrary(t, db, "shared-lib")
	seedMText(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form",
		Token{Kind: KindInclude, Value: "Form_T1000_de", Line: 8},
		Token{Kind: KindInclude, Value: "Shared_Footer", Line: 14},
		Token{Kind: KindInclude, Value: "Form_Attachment", Line: 24})
	seedMText(t, db, "letters", "Blocks/#Form_T1000_de.mtxm", "Form_T1000_de")
	seedMText(t, db, "letters", "Forms/Form_Attachment.mtxt", "Form_Attachment")
	// The library has its own copy of the home block, which must not land,
	// and the one footer the template needs from it.
	seedMText(t, db, "shared-lib", "Blocks/#Form_T1000_de.mtxm", "Form_T1000_de")
	seedMText(t, db, "shared-lib", "Blocks/Footnotes/#Shared_Footer.mtxm", "Shared_Footer")

	got, err := Included(context.Background(), db, "letters", "Forms/Demo_Form.mtxt", 1, 20, []string{"letters", "shared-lib"})
	if err != nil {
		t.Fatalf("Included: %v", err)
	}
	want := []string{
		"letters/Blocks/#Form_T1000_de.mtxm@1-10=Form_T1000_de",
		"shared-lib/Blocks/Footnotes/#Shared_Footer.mtxm@1-10=Shared_Footer",
	}
	if g := targets(got); strings.Join(g, " ") != strings.Join(want, " ") {
		t.Fatalf("included:\n got %q\nwant %q", g, want)
	}
}

// A copy outside the turn lands nowhere and decides nothing: another
// product's repository is never a target, nor is a library the turn does not
// reach, and a turn without a ceiling stays at home.
func TestIncludedStaysInsideTheTurn(t *testing.T) {
	db := edgeDB(t, "letters", "shared-lib", "other-lib", "claims-letters")
	markLibrary(t, db, "shared-lib")
	markLibrary(t, db, "other-lib")
	seedMText(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form",
		Token{Kind: KindInclude, Value: "Shared_Footer", Line: 3},
		Token{Kind: KindInclude, Value: "Claims_Only", Line: 4})
	seedMText(t, db, "shared-lib", "Blocks/#Shared_Footer.mtxm", "Shared_Footer")
	seedMText(t, db, "other-lib", "Blocks/#Shared_Footer.mtxm", "Shared_Footer")
	seedMText(t, db, "claims-letters", "Blocks/#Shared_Footer.mtxm", "Shared_Footer")
	seedMText(t, db, "claims-letters", "Blocks/#Claims_Only.mtxm", "Claims_Only")

	got, err := Included(context.Background(), db, "letters", "Forms/Demo_Form.mtxt", 1, 10, []string{"letters", "shared-lib", "claims-letters"})
	if err != nil {
		t.Fatalf("Included: %v", err)
	}
	if g := targets(got); strings.Join(g, " ") != "shared-lib/Blocks/#Shared_Footer.mtxm@1-10=Shared_Footer" {
		t.Errorf("included = %q, want the turn's library alone", g)
	}

	home, err := Included(context.Background(), db, "letters", "Forms/Demo_Form.mtxt", 1, 10, nil)
	if err != nil {
		t.Fatalf("Included: %v", err)
	}
	if len(home) != 0 {
		t.Errorf("without a ceiling an include left home: %q", targets(home))
	}
}

// Every file a plain include's stem names lands, however many there are: a
// stem is a file name, not a word five unrelated files define.
func TestIncludedHasNoDefinerCeiling(t *testing.T) {
	db := edgeDB(t, "letters")
	seedMText(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form", Token{Kind: KindInclude, Value: "Form_A", Line: 2})
	for _, dir := range []string{"Blocks", "Old", "Older", "Oldest", "Archive"} {
		seedMText(t, db, "letters", dir+"/#Form_A.mtxm", "Form_A")
	}

	got, err := Included(context.Background(), db, "letters", "Forms/Demo_Form.mtxt", 2, 2, nil)
	if err != nil {
		t.Fatalf("Included: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("included = %q, want all five files", targets(got))
	}
}

// M/TEXT runs on Windows: a stem is matched without regard to case, beyond
// ASCII and in the language code a variable stands for too. The landing names
// the file's own spelling.
func TestIncludedMatchesStemsIgnoringCase(t *testing.T) {
	db := edgeDB(t, "letters")
	seedMText(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form",
		Token{Kind: KindInclude, Value: "FORM_Ä", Line: 2},
		Token{Kind: KindInclude, Value: "Form_T1000_{lang}", Line: 3})
	seedMText(t, db, "letters", "Blocks/#Form_ä.mtxm", "Form_ä")
	seedMText(t, db, "letters", "Blocks/#Form_T1000_DE.mtxm", "Form_T1000_DE")

	got, err := Included(context.Background(), db, "letters", "Forms/Demo_Form.mtxt", 1, 5, nil)
	if err != nil {
		t.Fatalf("Included: %v", err)
	}
	want := "letters/Blocks/#Form_ä.mtxm@1-10=Form_ä letters/Blocks/#Form_T1000_DE.mtxm@1-10=Form_T1000_DE"
	if g := strings.Join(targets(got), " "); g != want {
		t.Errorf("included:\n got %s\nwant %s", g, want)
	}
}

// A language variable is one include: it lands one variant, never a mix of
// repositories. The repository is chosen for the whole include, home first,
// and in it the first variant that exists of de, fr, it, en.
func TestIncludedLandsOneLanguageVariant(t *testing.T) {
	for name, tc := range map[string]struct {
		home, lib []string
		want      string
	}{
		"home wins over a library's better language": {
			home: []string{"fr", "it"}, lib: []string{"de"},
			want: "letters/Blocks/#Form_T1000_fr.mtxm@1-10=Form_T1000_fr",
		},
		"all four at home": {
			home: []string{"en", "it", "fr", "de"},
			want: "letters/Blocks/#Form_T1000_de.mtxm@1-10=Form_T1000_de",
		},
		"none at home": {
			lib:  []string{"en", "it"},
			want: "shared-lib/Blocks/#Form_T1000_it.mtxm@1-10=Form_T1000_it",
		},
		"none anywhere": {},
	} {
		t.Run(name, func(t *testing.T) {
			db := edgeDB(t, "letters", "shared-lib")
			markLibrary(t, db, "shared-lib")
			seedMText(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form",
				Token{Kind: KindInclude, Value: "Form_T1000_{lang}", Line: 2})
			for _, l := range tc.home {
				seedMText(t, db, "letters", "Blocks/#Form_T1000_"+l+".mtxm", "Form_T1000_"+l)
			}
			for _, l := range tc.lib {
				seedMText(t, db, "shared-lib", "Blocks/#Form_T1000_"+l+".mtxm", "Form_T1000_"+l)
			}

			got, err := Included(context.Background(), db, "letters", "Forms/Demo_Form.mtxt", 1, 5, []string{"letters", "shared-lib"})
			if err != nil {
				t.Fatalf("Included: %v", err)
			}
			if g := strings.Join(targets(got), " "); g != tc.want {
				t.Errorf("included:\n got %s\nwant %s", g, tc.want)
			}
		})
	}
}

// A file whose stem matches lands only through a chunk: one indexed as
// skipped has none, and is no target.
func TestIncludedNeedsAChunk(t *testing.T) {
	db := edgeDB(t, "letters")
	seedMText(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form", Token{Kind: KindInclude, Value: "Form_A", Line: 2})
	if _, err := db.Exec(`INSERT INTO files (repo, path, sha, lang, skip_reason, stem) VALUES ('letters', 'Blocks/#Form_A.mtxm', 'x', ?, 'empty', 'form_a')`,
		symbols.MTextLang); err != nil {
		t.Fatal(err)
	}
	got, err := Included(context.Background(), db, "letters", "Forms/Demo_Form.mtxt", 1, 5, nil)
	if err != nil {
		t.Fatalf("Included: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("included = %q, want nothing", targets(got))
	}
}

// A parked repository is no target, not even the template's own library.
func TestIncludedIgnoresAParkedLibrary(t *testing.T) {
	db := edgeDB(t, "letters", "shared-lib")
	markLibrary(t, db, "shared-lib")
	if _, err := db.Exec(`UPDATE repo_state SET enabled = 0 WHERE name = 'shared-lib'`); err != nil {
		t.Fatal(err)
	}
	seedMText(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form", Token{Kind: KindInclude, Value: "Shared_Footer", Line: 1})
	seedMText(t, db, "shared-lib", "Blocks/#Shared_Footer.mtxm", "Shared_Footer")

	got, err := Included(context.Background(), db, "letters", "Forms/Demo_Form.mtxt", 1, 1, []string{"letters", "shared-lib"})
	if err != nil {
		t.Fatalf("Included: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a parked library was a target: %q", targets(got))
	}
}
