package threads

import (
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

func TestScopeSurvivesAReload(t *testing.T) {
	// The thread is a record. A reader coming back has to see why the answer
	// covered what it covered, and a resumed turn rebuilds its prompt rules
	// from this row.
	s, ctx, threadID, _ := newThreadStore(t)
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "How do loom and rongo differ?")
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
	msg, err := s.AddQuestion(ctx, threadID, "ba", "en", "How does indexing work?")
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
