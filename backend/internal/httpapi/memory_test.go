package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/memory"
	"github.com/trick77/rongo/internal/threads"
)

// memoryServer is a dev-auth server over a real thread store and a real
// memory store, with the fake asker the test configures.
func memoryServer(t *testing.T, f *fakeAsker) (*Server, *threads.Store, *memory.Store, *sql.DB) {
	t.Helper()
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	for _, subject := range []string{testSubject, otherSubject} {
		if _, err := svc.UpsertUser(subject, subject+"@example.invalid", true); err != nil {
			t.Fatalf("seed user %q: %v", subject, err)
		}
	}
	st := threads.NewStore(db)
	ms := memory.NewStore(db)
	return NewServer(Deps{Auth: svc, Ask: f, Threads: st, Memory: ms}), st, ms, db
}

func TestAsk_aDirectiveIsSavedLinkedToTheTurnAndAnnounced(t *testing.T) {
	// Given a turn whose understanding read a standing instruction
	f := &fakeAsker{tokens: []string{"Noted."}, directive: &memory.Directive{Text: "Never draw flowchart diagrams."}}
	srv, st, ms, _ := memoryServer(t, f)

	// When
	body := doSSE(t, srv, "/api/ask", `{"question":"Zeig mir nie wieder Flowcharts.","audience":"ba","language":"de"}`)

	// Then the rule is stored under the reader ...
	rows, err := ms.List(context.Background(), testSubject)
	if err != nil || len(rows) != 1 || rows[0].Text != "Never draw flowchart diagrams." {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	// ... the browser was told, with the id the undo needs ...
	var announced bool
	for _, ev := range events(body) {
		if ev[0] == "memory" {
			announced = true
			var got wireMemory
			if err := json.Unmarshal([]byte(ev[1]), &got); err != nil || got.ID != rows[0].ID || got.Text != rows[0].Text {
				t.Fatalf("memory event = %s (%v)", ev[1], err)
			}
		}
	}
	if !announced {
		t.Fatalf("no memory event in the stream:\n%s", body)
	}
	// ... and the turn carries it on a reload.
	list, _ := st.List(context.Background(), testSubject)
	msgs, _ := st.Messages(context.Background(), testSubject, list[0].ID)
	if len(msgs) != 1 || msgs[0].Memory == nil || msgs[0].Memory.ID != rows[0].ID {
		t.Fatalf("messages = %+v", msgs)
	}
	if f.remembered == nil || f.remembered.Row.ID != rows[0].ID {
		t.Fatalf("the pipeline was not handed what was stored: %+v", f.remembered)
	}
	if !f.memoryOn {
		t.Fatal("the turn ran with memory off")
	}
}

func TestAsk_theTurnReadsTheReadersRulesAndTheNextTurnSeesTheNewOne(t *testing.T) {
	f := &fakeAsker{tokens: []string{"x"}, directive: &memory.Directive{Text: "Keep it short."}}
	srv, _, ms, _ := memoryServer(t, f)
	if _, err := ms.Add(context.Background(), testSubject, memory.Directive{Text: "Never draw flowchart diagrams."}, 0); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	if _, err := ms.Add(context.Background(), otherSubject, memory.Directive{Text: "Theirs."}, 0); err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	doSSE(t, srv, "/api/ask", `{"question":"How?","audience":"ba"}`)
	if len(f.memoryRows) != 1 || f.memoryRows[0].Text != "Never draw flowchart diagrams." {
		t.Fatalf("first turn's rows = %+v, want the reader's one rule and nobody else's", f.memoryRows)
	}

	f.directive = nil
	doSSE(t, srv, "/api/ask", `{"question":"How?","audience":"ba"}`)
	if len(f.memoryRows) != 2 || f.memoryRows[0].Text != "Keep it short." {
		t.Fatalf("second turn's rows = %+v, want the rule the first turn gave, first", f.memoryRows)
	}
}

func TestAsk_withoutMemoryTheTurnRunsWithNone(t *testing.T) {
	f := &fakeAsker{tokens: []string{"x"}, directive: &memory.Directive{Text: "Never draw flowchart diagrams."}}
	srv := newTestServer(t, func(a *fakeAsker) { *a = *f })

	body := doSSE(t, srv, "/api/ask", `{"question":"How?","audience":"ba"}`)

	if strings.Contains(body, "event: memory") {
		t.Fatalf("memory off, yet a memory event:\n%s", body)
	}
	for _, ev := range events(body) {
		if ev[0] == "error" {
			t.Fatalf("the turn failed: %s", ev[1])
		}
	}
}

// TestAsk_theStoreRefusesThe41stRuleThroughTheHandler: the cap holds on the
// real store behind the real handler. The fake asker propagates the refusal
// as a failed turn; the graceful path, a templated line and a question that
// still runs, is the pipeline's and is tested in package ask.
func TestAsk_theStoreRefusesThe41stRuleThroughTheHandler(t *testing.T) {
	f := &fakeAsker{tokens: []string{"x"}, directive: &memory.Directive{Text: "One more."}}
	srv, _, ms, _ := memoryServer(t, f)
	for i := 0; i < memory.MaxRows; i++ {
		if _, err := ms.Add(context.Background(), testSubject, memory.Directive{Text: fmt.Sprintf("Rule %d.", i)}, 0); err != nil {
			t.Fatalf("seed rule %d: %v", i, err)
		}
	}
	body := doSSE(t, srv, "/api/ask", `{"question":"One more.","audience":"ba"}`)
	if !strings.Contains(body, "event: error") {
		t.Fatalf("the fake returns the refusal as an error; expected it on the stream:\n%s", body)
	}
	rows, _ := ms.List(context.Background(), testSubject)
	if len(rows) != memory.MaxRows {
		t.Fatalf("%d rows, want the cap untouched", len(rows))
	}
}

func TestMemory_listsTheReadersRulesNewestFirst(t *testing.T) {
	srv, _, ms, _ := memoryServer(t, &fakeAsker{})
	ctx := context.Background()
	for _, text := range []string{"First.", "Second."} {
		if _, err := ms.Add(ctx, testSubject, memory.Directive{Text: text}, 0); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := ms.Add(ctx, otherSubject, memory.Directive{Text: "Theirs."}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := act(srv, http.MethodGet, "/api/memory", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got memoryPage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled || len(got.Memories) != 2 || got.Memories[0].Text != "Second." || got.Memories[1].Text != "First." {
		t.Fatalf("page = %+v", got)
	}
}

func TestMemory_offSaysSoAndForgetsNothing(t *testing.T) {
	srv, _ := threadActions(t)

	rec := act(srv, http.MethodGet, "/api/memory", "")
	var got memoryPage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Enabled || len(got.Memories) != 0 {
		t.Fatalf("page = %s (%v)", rec.Body.String(), err)
	}
	if rec := act(srv, http.MethodDelete, "/api/memory/1", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("delete with memory off = %d", rec.Code)
	}
}

func TestForgetMemory_deletesMineAndAnswersOne404ForTheRest(t *testing.T) {
	srv, _, ms, _ := memoryServer(t, &fakeAsker{})
	ctx := context.Background()
	mine, _ := ms.Add(ctx, testSubject, memory.Directive{Text: "Mine."}, 0)
	theirs, _ := ms.Add(ctx, otherSubject, memory.Directive{Text: "Theirs."}, 0)

	if rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/memory/%d", mine.Row.ID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete mine = %d: %s", rec.Code, rec.Body.String())
	}
	if rows, _ := ms.List(ctx, testSubject); len(rows) != 0 {
		t.Fatalf("still there: %+v", rows)
	}
	for _, path := range []string{
		fmt.Sprintf("/api/memory/%d", theirs.Row.ID),
		fmt.Sprintf("/api/memory/%d", mine.Row.ID),
		"/api/memory/abc",
		"/api/memory/0",
	} {
		if rec := act(srv, http.MethodDelete, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("DELETE %s = %d, want one 404 for gone, not mine and nonsense alike", path, rec.Code)
		}
	}
	if rows, _ := ms.List(ctx, otherSubject); len(rows) != 1 {
		t.Fatalf("the other reader's rule went: %+v", rows)
	}
}

func TestPublicShare_carriesNoMemoryChip(t *testing.T) {
	f := &fakeAsker{tokens: []string{"So [1]."}, directive: &memory.Directive{Text: "Never draw flowchart diagrams."}}
	srv, st, _, _ := memoryServer(t, f)
	doSSE(t, srv, "/api/ask", `{"question":"How, and never show flowcharts again?","audience":"ba"}`)
	list, _ := st.List(context.Background(), testSubject)
	sh := share(t, srv, list[0].PublicID)

	rec := getPublic(srv, "/api/shares/"+sh.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"memory"`) {
		t.Fatalf("the shared page carries the owner's rule:\n%s", rec.Body.String())
	}
	// The owner's own read does.
	own := act(srv, http.MethodGet, "/api/threads/"+list[0].PublicID, "")
	if !strings.Contains(own.Body.String(), `"memory":{"id":`) {
		t.Fatalf("the owner's read lacks the chip:\n%s", own.Body.String())
	}
}

func TestReexplain_runsUnderTheReadersRules(t *testing.T) {
	f := &fakeAsker{tokens: []string{"x"}, reexplainTokens: []string{"y"}}
	srv, st, ms, db := memoryServer(t, f)
	ctx := context.Background()
	if _, err := ms.Add(ctx, testSubject, memory.Directive{Text: "Never draw flowchart diagrams."}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	chunkID := seedChunk(t, db)
	th, _ := st.Create(ctx, testSubject, "How?")
	msg, _ := st.AddQuestion(ctx, th.ID, "ba", "en", "How?", 0)
	if err := st.Finish(ctx, msg.ID, "So [1].", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := st.SaveSources(ctx, msg.ID, []ask.Source{{ChunkID: chunkID, Reason: "hit"}}); err != nil {
		t.Fatalf("sources: %v", err)
	}

	doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", msg.ID), `{"audience":"dev"}`)
	if len(f.memoryRows) != 1 {
		t.Fatalf("re-explain rows = %+v, want the reader's rule", f.memoryRows)
	}
}

// TestAsk_aTemplatedAnswerReachesTheBrowserLive: a turn with no sources
// streams nothing, so its text went only to the record and the reader saw
// an empty answer until a reload. finishTurn sends it whole, once, and says
// the turn is sourceless so the page hides the re-explain action.
func TestAsk_aTemplatedAnswerReachesTheBrowserLive(t *testing.T) {
	// The fake answers with no sources and streams nothing: the shape of
	// nothing-found, no-changes and a rule kept.
	srv := newTestServer(t, func(a *fakeAsker) { a.silentText = "I found nothing about this in the indexed code." })

	body := doSSE(t, srv, "/api/ask", `{"question":"How?","audience":"ba"}`)

	var tokens int
	var doneSourceless bool
	for _, ev := range events(body) {
		switch ev[0] {
		case "token":
			tokens++
			if !strings.Contains(ev[1], "found nothing") {
				t.Fatalf("token = %s", ev[1])
			}
		case "done":
			doneSourceless = strings.Contains(ev[1], `"sourceless":true`)
		}
	}
	if tokens != 1 {
		t.Fatalf("%d token events, want the templated text once:\n%s", tokens, body)
	}
	if !doneSourceless {
		t.Fatalf("done does not say the turn is sourceless:\n%s", body)
	}

	// A streamed answer is not sent twice, and is not sourceless.
	srv = newTestServer(t, func(a *fakeAsker) {
		a.tokens = []string{"So ", "[1]."}
		a.sources = []ask.Source{{ChunkID: 1, Reason: "hit"}}
	})
	body = doSSE(t, srv, "/api/ask", `{"question":"How?","audience":"ba"}`)
	tokens = 0
	for _, ev := range events(body) {
		if ev[0] == "token" {
			tokens++
		}
		if ev[0] == "done" && strings.Contains(ev[1], `"sourceless":true`) {
			t.Fatalf("a streamed answer with sources reads as sourceless:\n%s", body)
		}
	}
	if tokens != 2 {
		t.Fatalf("%d token events, want the two streamed and nothing appended", tokens)
	}
}
