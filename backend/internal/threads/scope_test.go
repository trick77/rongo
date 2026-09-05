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

// TestThreadScopeIsTheNarrowingAnEarlierTurnMade is the funnel: a follow-up
// names no repository because the reader named it a turn ago, and without this
// it would be asked which repository was meant all over again.
func TestThreadScopeIsTheNarrowingAnEarlierTurnMade(t *testing.T) {
	// Given a thread whose first turn answered out of one repository.
	s, ctx, threadID, _ := newThreadStore(t)
	first, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does rongo cite sources?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.SetScope(ctx, first.ID, ask.Scope{Known: []string{"rongo"}}); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	// When a later turn asks what the thread has narrowed to.
	got, err := s.ThreadScope(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("thread scope: %v", err)
	}

	// Then it is the repository that turn answered out of.
	if len(got) != 1 || got[0] != "rongo" {
		t.Errorf("thread scope = %v, want the repository the thread narrowed to", got)
	}
}

// TestThreadScopeTakesTheNewestNarrowing: a thread only ever narrows, so the
// most recent turn that named repositories is the narrowest.
func TestThreadScopeTakesTheNewestNarrowing(t *testing.T) {
	// Given two turns, the second narrower than the first.
	s, ctx, threadID, _ := newThreadStore(t)
	first, err := s.AddQuestion(ctx, threadID, "ba", "en", "How do peeq and rongo differ?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.SetScope(ctx, first.ID, ask.Scope{Known: []string{"peeq", "rongo"}}); err != nil {
		t.Fatalf("set scope: %v", err)
	}
	second, err := s.AddQuestion(ctx, threadID, "ba", "en", "And in rongo alone?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.SetScope(ctx, second.ID, ask.Scope{Known: []string{"rongo"}}); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	// When the pin is read.
	got, err := s.ThreadScope(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("thread scope: %v", err)
	}

	// Then it is the newer, narrower one.
	if len(got) != 1 || got[0] != "rongo" {
		t.Errorf("thread scope = %v, want the newest narrowing", got)
	}
}

// TestThreadScopeIgnoresAnAllRepositoriesTurn: "all repositories" narrows
// nothing, so it pins nothing — and an older, narrower turn still wins.
func TestThreadScopeIgnoresAnAllRepositoriesTurn(t *testing.T) {
	// Given a narrowed turn followed by one the reader opened up on purpose.
	s, ctx, threadID, _ := newThreadStore(t)
	first, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does rongo cite sources?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.SetScope(ctx, first.ID, ask.Scope{Known: []string{"rongo"}}); err != nil {
		t.Fatalf("set scope: %v", err)
	}
	second, err := s.AddQuestion(ctx, threadID, "ba", "en", "In all repos, how is pricing resolved?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.SetScope(ctx, second.ID, ask.Scope{All: true}); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	// When the pin is read, Then the All turn is skipped: it narrows nothing
	// of its own and it does not end the thread's narrowing either.
	got, err := s.ThreadScope(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("thread scope: %v", err)
	}
	if len(got) != 1 || got[0] != "rongo" {
		t.Errorf("thread scope = %v, want the narrowing an All turn cannot undo", got)
	}
}

// TestThreadScopeOfAFreshThreadIsEmpty: the first turn of a thread has nothing
// to inherit, and the ladder runs whole.
func TestThreadScopeOfAFreshThreadIsEmpty(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)
	got, err := s.ThreadScope(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("thread scope: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("thread scope = %v, want nothing for a thread that never narrowed", got)
	}
}

// TestThreadScopeOfSomeoneElsesThreadIsEmpty: the id comes from the browser,
// and a thread belongs to the person who asked.
func TestThreadScopeOfSomeoneElsesThreadIsEmpty(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does rongo cite sources?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.SetScope(ctx, msg.ID, ask.Scope{Known: []string{"rongo"}}); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	got, err := s.ThreadScope(ctx, "someone-else", threadID)
	if err != nil {
		t.Fatalf("thread scope: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("thread scope = %v, want nothing for a thread that is not theirs", got)
	}
}

// TestLastTurnIsWhatAFollowUpIsAFollowUpTo: the newest turn that actually
// answered, which is what "das" in the next question points at.
func TestLastTurnIsWhatAFollowUpIsAFollowUpTo(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)
	first, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does rongo cite sources?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Finish(ctx, first.ID, "Every claim carries repo, file and line.", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	got, ok, err := s.LastTurn(ctx, testSubject, threadID)
	if err != nil || !ok {
		t.Fatalf("last turn: %v ok=%v", err, ok)
	}
	if got.Question != "How does rongo cite sources?" {
		t.Errorf("question = %q, want the one the thread last asked", got.Question)
	}
	if got.Answer != "Every claim carries repo, file and line." {
		t.Errorf("answer = %q, want the one it got", got.Answer)
	}
}

// TestLastTurnSkipsATurnThatNeverAnswered: a turn that failed and a turn that
// asked back are not something a later question can point at. Both leave the
// answer column empty, and the turn before them is still the last one.
func TestLastTurnSkipsATurnThatNeverAnswered(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)
	answered, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does rongo cite sources?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Finish(ctx, answered.ID, "Every claim carries repo, file and line.", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	failed, err := s.AddQuestion(ctx, threadID, "ba", "en", "Und wie schnell?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Fail(ctx, failed.ID, "the turn failed"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	got, ok, err := s.LastTurn(ctx, testSubject, threadID)
	if err != nil || !ok {
		t.Fatalf("last turn: %v ok=%v", err, ok)
	}
	if got.ID != answered.ID {
		t.Errorf("last turn = %d, want the last one that answered (%d)", got.ID, answered.ID)
	}
}

// TestLastTurnOfAFreshOrForeignThreadIsNothing: nothing to point at, and a
// thread belongs to the person who asked.
func TestLastTurnOfAFreshOrForeignThreadIsNothing(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)
	if _, ok, err := s.LastTurn(ctx, testSubject, threadID); err != nil || ok {
		t.Errorf("fresh thread: ok=%v err=%v, want nothing to follow up on", ok, err)
	}

	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does rongo cite sources?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := s.Finish(ctx, msg.ID, "An answer.", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, ok, err := s.LastTurn(ctx, "someone-else", threadID); err != nil || ok {
		t.Errorf("foreign thread: ok=%v err=%v, want nothing", ok, err)
	}
}
