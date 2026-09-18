package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, 4); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, s := range []string{"jan", "other"} {
		if _, err := db.Exec(`INSERT INTO users (subject, email) VALUES (?, ?)`, s, s+"@x.invalid"); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	// One project of two repositories and one of one: the scope tests need
	// a project name that is not a repository name.
	for _, r := range [][3]string{{"shop-backend", "shop", "backend"}, {"shop-ui", "shop", "ui"}, {"peeq", "peeq", ""}} {
		if _, err := db.Exec(`INSERT INTO repo_state (name, clone_url, project, part) VALUES (?, ?, ?, ?)`,
			r[0], "https://x/"+r[0], r[1], r[2]); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	return db
}

func TestAdd_writesTheRuleAndListReadsItBack(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()

	added, err := s.Add(ctx, "jan", Directive{Text: "Never draw flowchart diagrams."}, 0)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added.Row.ID == 0 || added.Row.Text != "Never draw flowchart diagrams." {
		t.Fatalf("added = %+v", added)
	}
	rows, err := s.List(ctx, "jan")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Text != added.Row.Text || !rows[0].ScopeLive {
		t.Fatalf("rows = %+v", rows)
	}
	// Another reader's list is their own.
	other, _ := s.List(ctx, "other")
	if len(other) != 0 {
		t.Fatalf("other reader sees %d rows", len(other))
	}
}

func TestAdd_replacesAndRemovesOnlyTheReadersOwnRows(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()
	old, _ := s.Add(ctx, "jan", Directive{Text: "Always draw a flowchart."}, 0)
	theirs, _ := s.Add(ctx, "other", Directive{Text: "Theirs."}, 0)
	gone, _ := s.Add(ctx, "jan", Directive{Text: "Keep answers short."}, 0)

	added, err := s.Add(ctx, "jan", Directive{
		Text:     "Never draw flowchart diagrams.",
		Replaces: []int64{old.Row.ID, theirs.Row.ID},
		Removes:  []int64{gone.Row.ID, 9999},
	}, 0)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(added.Replaced) != 1 || added.Replaced[0] != "Always draw a flowchart." {
		t.Fatalf("replaced = %v", added.Replaced)
	}
	if len(added.Removed) != 1 || added.Removed[0] != "Keep answers short." {
		t.Fatalf("removed = %v", added.Removed)
	}
	if len(added.Deleted) != 2 {
		t.Fatalf("deleted = %v", added.Deleted)
	}
	rows, _ := s.List(ctx, "jan")
	if len(rows) != 1 || rows[0].ID != added.Row.ID {
		t.Fatalf("rows = %+v", rows)
	}
	other, _ := s.List(ctx, "other")
	if len(other) != 1 {
		t.Fatalf("the other reader's row went: %+v", other)
	}
}

func TestAdd_resolvesTheScopeOrDropsIt(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()

	project, err := s.Add(ctx, "jan", Directive{Text: "Ignore the legacy module.", Scope: "Shop"}, 0)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if project.Row.Scope != "shop" || project.ScopeDropped != "" {
		t.Fatalf("project scope = %+v", project)
	}
	repo, _ := s.Add(ctx, "jan", Directive{Text: "Skip the tests.", Scope: "shop-ui"}, 0)
	if repo.Row.Scope != "shop-ui" {
		t.Fatalf("repo scope = %+v", repo)
	}
	unknown, _ := s.Add(ctx, "jan", Directive{Text: "Nothing.", Scope: "warehouse"}, 0)
	if unknown.Row.Scope != "" || unknown.ScopeDropped != "warehouse" {
		t.Fatalf("unknown scope = %+v", unknown)
	}

	rows, _ := s.List(ctx, "jan")
	byText := map[string]Row{}
	for _, r := range rows {
		byText[r.Text] = r
	}
	if got := byText["Ignore the legacy module."]; !got.ScopeLive || strings.Join(got.members, ",") != "shop-backend,shop-ui" {
		t.Fatalf("project members = %+v", got)
	}
	if got := byText["Skip the tests."]; !got.ScopeLive || strings.Join(got.members, ",") != "shop-ui" {
		t.Fatalf("repo members = %+v", got)
	}
}

func TestList_aScopeThatLeftTheIndexIsNotLive(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()
	if _, err := s.Add(ctx, "jan", Directive{Text: "Skip the tests.", Scope: "peeq"}, 0); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM repo_state WHERE name = 'peeq'`); err != nil {
		t.Fatalf("drop repo: %v", err)
	}
	rows, _ := s.List(ctx, "jan")
	if len(rows) != 1 || rows[0].Scope != "peeq" || rows[0].ScopeLive {
		t.Fatalf("rows = %+v", rows)
	}
	// And it applies everywhere rather than nowhere.
	if !Applies(rows[0], []string{"shop-ui"}) {
		t.Fatal("a dead scope must not silence the rule")
	}
}

func TestAdd_refusesThe41stRuleAndKeepsTheOthers(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()
	for i := 0; i < MaxRows; i++ {
		if _, err := s.Add(ctx, "jan", Directive{Text: strings.Repeat("x", i+1)}, 0); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	_, err := s.Add(ctx, "jan", Directive{Text: "One more."}, 0)
	if !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v, want ErrFull", err)
	}
	rows, _ := s.List(ctx, "jan")
	if len(rows) != MaxRows {
		t.Fatalf("%d rows after the refusal, want %d", len(rows), MaxRows)
	}
	// A replacement still lands: it frees its own slot first.
	added, err := s.Add(ctx, "jan", Directive{Text: "Replacement.", Replaces: []int64{rows[0].ID}}, 0)
	if err != nil || added.Row.ID == 0 {
		t.Fatalf("replace at the cap: %+v %v", added, err)
	}
}

func TestAdd_forgettingOnlyWritesNoRow(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()
	old, _ := s.Add(ctx, "jan", Directive{Text: "Never draw flowchart diagrams."}, 0)
	added, err := s.Add(ctx, "jan", Directive{Removes: []int64{old.Row.ID}}, 0)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added.Row.ID != 0 || len(added.Removed) != 1 {
		t.Fatalf("added = %+v", added)
	}
	rows, _ := s.List(ctx, "jan")
	if len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRemove_isOwnerBlindInWhatItSays(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()
	mine, _ := s.Add(ctx, "jan", Directive{Text: "Mine."}, 0)
	theirs, _ := s.Add(ctx, "other", Directive{Text: "Theirs."}, 0)

	if ok, err := s.Remove(ctx, "jan", theirs.Row.ID); err != nil || ok {
		t.Fatalf("removing another reader's rule: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Remove(ctx, "jan", 9999); err != nil || ok {
		t.Fatalf("removing nothing: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Remove(ctx, "jan", mine.Row.ID); err != nil || !ok {
		t.Fatalf("removing mine: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.Remove(ctx, "jan", mine.Row.ID); ok {
		t.Fatal("removed twice")
	}
}

func TestList_linksTheThreadTheRuleWasSaidIn(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()
	// Seeded by hand: the threads package imports ask, which imports this
	// one, so its store cannot be used from here.
	res, err := db.Exec(`INSERT INTO threads (user_subject, title, public_id) VALUES ('jan', 'Flowcharts', 'abcdefghijklmnopqrstuv')`)
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	threadID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO messages (thread_id, ordinal, audience, language, question) VALUES (?, 0, 'ba', 'de', 'Zeig mir nie wieder Flowcharts.')`, threadID)
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	msgID, _ := res.LastInsertId()
	if _, err := s.Add(ctx, "jan", Directive{Text: "Never draw flowchart diagrams."}, msgID); err != nil {
		t.Fatalf("add: %v", err)
	}
	rows, _ := s.List(ctx, "jan")
	if len(rows) != 1 || rows[0].ThreadID != "abcdefghijklmnopqrstuv" {
		t.Fatalf("rows = %+v, want the thread's address", rows)
	}
	// The thread goes, the rule stays, the link is simply gone.
	if _, err := db.Exec(`DELETE FROM threads WHERE id = ?`, threadID); err != nil {
		t.Fatalf("delete thread: %v", err)
	}
	rows, _ = s.List(ctx, "jan")
	if len(rows) != 1 || rows[0].ThreadID != "" {
		t.Fatalf("after the thread went: %+v", rows)
	}
}

func TestSanitize_oneLineNoMarkup(t *testing.T) {
	got := Sanitize("  Never ```mermaid\n### draw\tflowcharts.  ")
	if got != "Never mermaid draw flowcharts." {
		t.Fatalf("got %q", got)
	}
	long := Sanitize(strings.Repeat("ä", MaxRunes+50))
	if n := len([]rune(long)); n != MaxRunes {
		t.Fatalf("%d runes, want %d", n, MaxRunes)
	}
	if Sanitize("`#`") != "" {
		t.Fatal("markup alone is nothing")
	}
}

func TestApplies_scopedRulesFollowTheTurnsRepositories(t *testing.T) {
	global := Row{ID: 1, Text: "g", ScopeLive: true}
	scoped := Row{ID: 2, Text: "s", Scope: "shop", ScopeLive: true, members: []string{"shop-backend", "shop-ui"}}

	if !Applies(global, []string{"peeq"}) {
		t.Fatal("global must apply everywhere")
	}
	if !Applies(scoped, nil) {
		t.Fatal("a whole-corpus turn touches every project")
	}
	if !Applies(scoped, []string{"peeq", "shop-ui"}) {
		t.Fatal("a member repository is inside the scope")
	}
	if Applies(scoped, []string{"peeq"}) {
		t.Fatal("another project is outside the scope")
	}
}

func TestBlock_isEmptyWithoutRulesAndListsThoseThatApply(t *testing.T) {
	if Block(nil, nil) != "" {
		t.Fatal("no rules must render nothing at all")
	}
	rows := []Row{
		{ID: 1, Text: "Never draw ```flowchart``` diagrams.", ScopeLive: true},
		{ID: 2, Text: "Skip tests.", Scope: "shop", ScopeLive: true, members: []string{"shop-ui"}},
	}
	got := Block(rows, []string{"peeq"})
	if !strings.Contains(got, "- Never draw flowchart diagrams.\n") {
		t.Fatalf("block = %q", got)
	}
	if strings.Contains(got, "Skip tests") {
		t.Fatalf("a rule scoped to another project leaked: %q", got)
	}
	if strings.Contains(got, "```") || strings.Contains(got, "#") {
		t.Fatalf("markup in the block: %q", got)
	}
	if !strings.Contains(got, "take\npriority over every rule above") {
		t.Fatalf("the block must say it outranks the prompt: %q", got)
	}
}

func TestListLine_namesIdsForTheUnderstandingStep(t *testing.T) {
	if ListLine(nil) != "" {
		t.Fatal("nothing saved renders nothing")
	}
	got := ListLine([]Row{{ID: 7, Text: "Never draw flowcharts."}, {ID: 8, Text: "Skip tests.", Scope: "shop"}})
	if !strings.Contains(got, "[7] Never draw flowcharts.\n") || !strings.Contains(got, "[8] Skip tests. (shop only)\n") {
		t.Fatalf("got %q", got)
	}
}

func TestHolder_applyFoldsTheDirectiveIn(t *testing.T) {
	h := NewHolder([]Row{{ID: 1, Text: "old"}, {ID: 2, Text: "keep"}})
	h.Apply(Added{Row: Row{ID: 3, Text: "new"}, Deleted: []int64{1}})
	rows := h.Rows()
	if len(rows) != 2 || rows[0].ID != 3 || rows[1].ID != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	var none *Holder
	if none.Rows() != nil {
		t.Fatal("a nil holder has no rows")
	}
	none.Apply(Added{})
	ctx := With(context.Background(), h)
	if From(ctx) != h {
		t.Fatal("the holder did not travel")
	}
	if From(context.Background()) != nil {
		t.Fatal("a bare context has no holder")
	}
}

func TestDirective_empty(t *testing.T) {
	if !(Directive{Text: " ` "}).Empty() {
		t.Fatal("markup alone is empty")
	}
	if (Directive{Removes: []int64{1}}).Empty() {
		t.Fatal("a removal is a directive")
	}
}

// TestAdd_survivesAWriterOnAnotherConnection: the title goroutine writes the
// thread's title while a rule is being kept. A transaction that opened with
// a read held a stale snapshot and its INSERT failed at once with "database
// is locked"; writing first takes the lock and waits instead.
func TestAdd_survivesAWriterOnAnotherConnection(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = db.Exec(`UPDATE users SET email = ? WHERE subject = 'other'`, fmt.Sprintf("t%d@x.invalid", i))
		}
	}()
	for i := 0; i < 30; i++ {
		if _, err := s.Add(ctx, "jan", Directive{Text: fmt.Sprintf("Rule %d.", i)}, 0); err != nil {
			t.Fatalf("add %d beside another writer: %v", i, err)
		}
	}
	close(stop)
	<-done
}
