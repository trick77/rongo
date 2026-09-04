package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
)

func TestAskSendsTheScopeNoticeAndRecordsIt(t *testing.T) {
	// Given a turn whose question named a repository the index does not carry.
	srv, st := newTestServerWithStore(t, func(f *fakeAsker) {
		f.tokens = []string{"rongo keeps no session [1]."}
		f.notice = `No repository called "loom" in the index. Answered for "rongo" alone.`
		f.scope = ask.Scope{Known: []string{"rongo"}, Unknown: []string{"loom"}}
	})

	// When
	body := doSSE(t, srv, "/api/ask", `{"question":"How do loom and rongo differ?","audience":"ba"}`)

	// Then the reader is told, in the stream.
	if !strings.Contains(body, "event: notice") {
		t.Fatalf("no notice event in the stream:\n%s", body)
	}
	if !strings.Contains(body, "loom") {
		t.Errorf("the notice does not name the missing repository:\n%s", body)
	}

	// And a reload finds it: the thread is a record, and the scope is why the
	// answer covered what it covered.
	ths, err := st.List(context.Background(), testSubject)
	if err != nil || len(ths) == 0 {
		t.Fatalf("list threads: %v (%d)", err, len(ths))
	}
	msgs, err := st.Messages(context.Background(), testSubject, ths[0].ID)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("messages: %v (%d)", err, len(msgs))
	}
	got := msgs[len(msgs)-1].Scope
	if len(got.Unknown) != 1 || got.Unknown[0] != "loom" {
		t.Errorf("stored scope = %+v, want the missing repository", got)
	}
}

func TestAskSendsNoNoticeOnAnOrdinaryTurn(t *testing.T) {
	srv, _ := newTestServerWithStore(t, func(f *fakeAsker) {
		f.tokens = []string{"It works like this [1]."}
	})

	body := doSSE(t, srv, "/api/ask", `{"question":"How does indexing work?","audience":"ba"}`)

	if strings.Contains(body, "event: notice") {
		t.Errorf("an ordinary turn must say nothing about its scope:\n%s", body)
	}
}

func TestResumingACardCarriesTheScopeItWasAskedUnder(t *testing.T) {
	// Without this the resumed turn re-answers "how do loom and rongo differ"
	// from rongo-only sources with no rule saying loom is not indexed, and the
	// model writes loom's side from its own training.
	srv, st := newTestServerWithStore(t, withAskerResuming())
	asker := srv.deps.Ask.(*fakeAsker)
	msgID, _ := seedClarification(t, st)
	if err := st.SetScope(context.Background(), msgID,
		ask.Scope{Known: []string{"rongo"}, Unknown: []string{"loom"}}); err != nil {
		t.Fatalf("set scope: %v", err)
	}

	body := doSSE(t, srv, "/api/ask",
		fmt.Sprintf(`{"question":"How do loom and rongo differ?","audience":"ba","clarification_message_id":%d,"choice":0}`, msgID))

	if !strings.Contains(body, "event: done") {
		t.Fatalf("the resumed turn did not finish:\n%s", body)
	}
	got := asker.gotScope
	if len(got.Unknown) != 1 || got.Unknown[0] != "loom" {
		t.Errorf("resumed turn was handed scope %+v, want the one the card was asked under", got)
	}
	// Said to the reader of the resumed turn, not only to the reader of the
	// card: the resumed answer is a turn of its own.
	if !strings.Contains(body, "event: notice") || !strings.Contains(body, "loom") {
		t.Errorf("the resumed turn did not report its scope:\n%s", body)
	}

	// And recorded on the NEW message, or a later re-explain of it reads an
	// empty scope and drops the rule that keeps the model off loom.
	ths, err := st.List(context.Background(), testSubject)
	if err != nil || len(ths) == 0 {
		t.Fatalf("list threads: %v (%d)", err, len(ths))
	}
	msgs, err := st.Messages(context.Background(), testSubject, ths[0].ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.ID == msgID {
		t.Fatal("the resumed turn did not add a message")
	}
	if len(last.Scope.Unknown) != 1 || last.Scope.Unknown[0] != "loom" {
		t.Errorf("resumed message stored scope %+v, want the card's", last.Scope)
	}
}
