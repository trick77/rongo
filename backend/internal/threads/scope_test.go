package threads

import (
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

func TestScopeSurvivesAReload(t *testing.T) {
	// The thread is a record. A reader coming back has to see why the answer
	// covered what it covered, and a resumed turn rebuilds its prompt rules
	// from this row.
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "How do loom and rongo differ?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}

	want := ask.Scope{Known: []string{"rongo"}, Unknown: []string{"loom"}}
	if err := s.SetScope(ctx, msg.ID, want); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	got, ok, err := s.Message(ctx, testSubject, msg.ID)
	if err != nil || !ok {
		t.Fatalf("read message: %v ok=%v", err, ok)
	}
	if len(got.Scope.Known) != 1 || got.Scope.Known[0] != "rongo" {
		t.Errorf("scope.Known = %v", got.Scope.Known)
	}
	if len(got.Scope.Unknown) != 1 || got.Scope.Unknown[0] != "loom" {
		t.Errorf("scope.Unknown = %v", got.Scope.Unknown)
	}

	// And through the thread listing, which is what a reload actually reads.
	all, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(all) == 0 || len(all[len(all)-1].Scope.Unknown) != 1 {
		t.Errorf("listed scope = %+v", all)
	}
}

func TestScopeOfAnOrdinaryTurnWritesNothing(t *testing.T) {
	// Most questions name no repository, and a row full of empty JSON would
	// make the column say something it does not know.
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does indexing work?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}

	if err := s.SetScope(ctx, msg.ID, ask.Scope{}); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	got, ok, err := s.Message(ctx, testSubject, msg.ID)
	if err != nil || !ok {
		t.Fatalf("read message: %v ok=%v", err, ok)
	}
	if len(got.Scope.Known) != 0 || len(got.Scope.Unknown) != 0 {
		t.Errorf("scope = %+v, want the zero value", got.Scope)
	}
}

func TestDocsOnlySurvivesAReloadWithoutANamedRepository(t *testing.T) {
	// The ordinary documentation-only turn names no repository at all, so the
	// "nothing to say, write nothing" shortcut above would drop the one thing
	// it does have to say — and the reader would see the notice while the turn
	// streamed and never again after a reload.
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "Which choices are locked?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}

	if err := s.SetScope(ctx, msg.ID, ask.Scope{DocsOnly: true}); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	got, ok, err := s.Message(ctx, testSubject, msg.ID)
	if err != nil || !ok {
		t.Fatalf("read message: %v ok=%v", err, ok)
	}
	if !got.Scope.DocsOnly {
		t.Error("scope.DocsOnly = false after a reload")
	}
	// The notice is rendered off the row, so this is what the reader gets back.
	if !strings.Contains(got.Notice, "documentation alone") {
		t.Errorf("notice = %q, want the documentation-only sentence", got.Notice)
	}
}

// TestScopeOfAnAllRepositoriesTurnIsWrittenEvenThoughItNamesNone is the same
// shortcut seen from the other side: "every repository" names none and is
// still a scope. Skipping it would leave a re-explain reading a zero scope,
// and the reader would be asked which repository they meant after already
// saying all of them.
func TestScopeOfAnAllRepositoriesTurnIsWrittenEvenThoughItNamesNone(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "in all repos, how are token costs calculated?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}

	if err := s.SetScope(ctx, msg.ID, ask.Scope{All: true}); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	got, ok, err := s.Message(ctx, testSubject, msg.ID)
	if err != nil || !ok {
		t.Fatalf("read message: %v ok=%v", err, ok)
	}
	if !got.Scope.All {
		t.Errorf("scope = %+v, want the all-repositories permission kept", got.Scope)
	}
}

// TestScopeStoredBeforeAllExistedStillReads: the column is plain JSON and old
// rows carry no all field. They must decode to "not every repository", never
// to a permission nobody granted.
func TestScopeStoredBeforeAllExistedStillReads(t *testing.T) {
	s, ctx, threadID, db := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "how does peeq sign in?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE messages SET scope = ? WHERE id = ?`, `{"Known":["peeq"],"Unknown":[]}`, msg.ID); err != nil {
		t.Fatalf("seed old scope: %v", err)
	}

	got, ok, err := s.Message(ctx, testSubject, msg.ID)
	if err != nil || !ok {
		t.Fatalf("read message: %v ok=%v", err, ok)
	}
	if got.Scope.All {
		t.Error("a row written before All existed must not grant it")
	}
	if len(got.Scope.Known) != 1 || got.Scope.Known[0] != "peeq" {
		t.Errorf("scope = %+v, want the stored repository", got.Scope)
	}
}
