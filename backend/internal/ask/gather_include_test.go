package ask

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/symbols"
)

// seedMTextIn records an M/TEXT file the way the indexer writes one: lang
// mtext, its folded stem, one chunk, no symbol, and the include tokens of its
// lines, folded as Extract folds them.
func seedMTextIn(t *testing.T, db *sql.DB, repo, path, stem string, end int, body string, includes map[string]int) int64 {
	t.Helper()
	id := seedChunkIn(t, db, repo, path, 0, 1, end, stem, body)
	if _, err := db.Exec(`UPDATE files SET lang = ?, stem = ? WHERE repo = ? AND path = ?`,
		symbols.MTextLang, symbols.MTextStemKey(path), repo, path); err != nil {
		t.Fatal(err)
	}
	for v, line := range includes {
		seedTokenIn(t, db, repo, path, "include", symbols.FoldStem(v), line)
	}
	return id
}

// A letter's wrapper includes its text block through the language variable,
// a name no symbol carries, so only the include step reaches it: one
// language variant lands, the block's own include follows a hop later, and
// the shared footer comes from the library the turn is built on.
func TestGather_followsAnIncludeFromTheWrapperToItsBlock(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "letters")
	seedRepo(t, db, "shared-lib")
	if _, err := db.Exec(`UPDATE repo_state SET library = 1 WHERE name = 'shared-lib'`); err != nil {
		t.Fatal(err)
	}
	wrapper := seedMTextIn(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form", 10,
		".DSC Payment reminder\n#Blocks\\Form_T1000_*HV_SPRACHE\n#\\\\Shared_Lib\\Blocks\\Shared_Footer",
		map[string]int{"Form_T1000_{lang}": 2, "Shared_Footer": 3})
	for _, l := range []string{"de", "fr", "it"} {
		var inc map[string]int
		if l == "de" {
			inc = map[string]int{"Form_Layout": 2}
		}
		seedMTextIn(t, db, "letters", "Blocks/#Form_T1000_"+l+".mtxm", "Form_T1000_"+l, 5,
			".DSC Overdue paragraph\n#Blocks\\Form_Layout", inc)
	}
	seedMTextIn(t, db, "letters", "Blocks/#Form_Layout.mtxm", "Form_Layout", 5, ".TABLE\n.TABLE END", nil)
	seedMTextIn(t, db, "shared-lib", "Blocks/#Shared_Footer.mtxm", "Shared_Footer", 5, "Kind regards", nil)

	g := NewGatherer(db, GatherOptions{MaxHops: 2, TokenBudget: 10000}).within([]string{"letters", "shared-lib"})
	got, err := g.Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, wrapper)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, want := range []struct{ repo, path, reason string }{
		{"letters", "Blocks/#Form_T1000_de.mtxm", "include:Form_T1000_de from letters/Forms/Demo_Form.mtxt"},
		{"shared-lib", "Blocks/#Shared_Footer.mtxm", "include:Shared_Footer from letters/Forms/Demo_Form.mtxt"},
		{"letters", "Blocks/#Form_Layout.mtxm", "include:Form_Layout from letters/Blocks/#Form_T1000_de.mtxm"},
	} {
		s, ok := sourceIn(got, want.repo, want.path)
		if !ok {
			t.Errorf("sources = %v, want %s/%s", repoPaths(got), want.repo, want.path)
			continue
		}
		if s.Reason != want.reason {
			t.Errorf("%s reason = %q, want %q", want.path, s.Reason, want.reason)
		}
	}
	if s, ok := sourceIn(got, "letters", "Blocks/#Form_Layout.mtxm"); ok && s.Hop != 2 {
		t.Errorf("the layout block came in at hop %d, want 2", s.Hop)
	}
	for _, l := range []string{"fr", "it"} {
		if hasIn(got, "letters", "Blocks/#Form_T1000_"+l+".mtxm") {
			t.Errorf("sources = %v, want one language variant, not %s too", repoPaths(got), l)
		}
	}
}

// Only what the gathered lines include is followed: a block the rest of the
// file names stays out.
func TestGather_followsOnlyTheIncludesOfTheGatheredLines(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "letters")
	hit := seedChunkIn(t, db, "letters", "Forms/Demo_Form.mtxt", 0, 1, 4, "Demo_Form", ".DSC Reminder\n#Blocks\\Form_A")
	seedChunkIn(t, db, "letters", "Forms/Demo_Form.mtxt", 1, 5, 9, "if", ".IF *x\n#Blocks\\Form_B\n.END")
	if _, err := db.Exec(`UPDATE files SET lang = 'mtext' WHERE path = 'Forms/Demo_Form.mtxt'`); err != nil {
		t.Fatal(err)
	}
	seedTokenIn(t, db, "letters", "Forms/Demo_Form.mtxt", "include", "form_a", 2)
	seedTokenIn(t, db, "letters", "Forms/Demo_Form.mtxt", "include", "form_b", 6)
	seedMTextIn(t, db, "letters", "Blocks/#Form_A.mtxm", "Form_A", 3, "Text A", nil)
	seedMTextIn(t, db, "letters", "Blocks/#Form_B.mtxm", "Form_B", 3, "Text B", nil)

	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 10000}).within([]string{"letters"})
	got, err := g.Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hit)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !hasIn(got, "letters", "Blocks/#Form_A.mtxm") {
		t.Errorf("sources = %v, want the block the hit includes", repoPaths(got))
	}
	if hasIn(got, "letters", "Blocks/#Form_B.mtxm") {
		t.Errorf("sources = %v, want no block from lines the turn never gathered", repoPaths(got))
	}
}

// An include head too large for what is left of the budget is skipped, not
// the end of the hop: the references after it keep their chance.
func TestGather_aRefusedIncludeLeavesTheReferences(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "letters")
	hit := seedMTextIn(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form", 3,
		"Helper "+strings.Repeat("x", 29), map[string]int{"Form_Big": 2}) // 9 tokens
	seedMTextIn(t, db, "letters", "Blocks/#Form_Big.mtxm", "Form_Big", 3, strings.Repeat("y", 400), nil) // 100 tokens
	seedChunkIn(t, db, "letters", "src/Helper.java", 0, 1, 5, "Helper", "void Helper(){}")               // 4 tokens
	seedSymbolIn(t, db, "letters", "src/Helper.java", "Helper", 1)

	// 25 of 30 is the walk's share: the hit and the reference fit, the head
	// does not.
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 30}).within([]string{"letters"})
	got, err := g.Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hit)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if hasIn(got, "letters", "Blocks/#Form_Big.mtxm") {
		t.Errorf("sources = %v, the oversized head was admitted", repoPaths(got))
	}
	if !hasIn(got, "letters", "src/Helper.java") {
		t.Errorf("sources = %v, want the reference after the refused include", repoPaths(got))
	}
}

// The include step spends the symbol walk's budget, never the crossing
// reserve: a landing that fits only the crossing's share stays out.
func TestGather_anIncludeSpendsTheWalksBudget(t *testing.T) {
	db := gatherDB(t)
	seedRepo(t, db, "letters")
	body := strings.Repeat("x", 36) // 9 tokens, each side
	hit := seedMTextIn(t, db, "letters", "Forms/Demo_Form.mtxt", "Demo_Form", 3, body, map[string]int{"Form_A": 2})
	seedMTextIn(t, db, "letters", "Blocks/#Form_A.mtxm", "Form_A", 3, body, nil)

	// 18 of 21 is the walk's share, 21 the crossing's: 9 + 9 fits the walk.
	g := NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 21}).within([]string{"letters"})
	got, err := g.Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hit)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !hasIn(got, "letters", "Blocks/#Form_A.mtxm") {
		t.Fatalf("sources = %v, want the include inside the walk's budget", repoPaths(got))
	}

	// 17 of 20 is the walk's share: 9 + 9 only fits the crossing's 20.
	g = NewGatherer(db, GatherOptions{MaxHops: 1, TokenBudget: 20}).within([]string{"letters"})
	got, err = g.Gather(context.Background(), []retrieve.Hit{hitInFor(t, db, hit)})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if hasIn(got, "letters", "Blocks/#Form_A.mtxm") {
		t.Errorf("sources = %v, want the include kept out of the crossing reserve", repoPaths(got))
	}
}
