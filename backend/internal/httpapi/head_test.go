package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
)

// headDeps is askDeps with the dev user already made: these tests seed a
// thread before the first request, so its owner has to exist by then.
func headDeps(t *testing.T, a Asker) (Deps, *threads.Store) {
	t.Helper()
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	if _, err := svc.UpsertUser(testSubject, "dev@x.invalid", false); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	st := threads.NewStore(db)
	return Deps{Auth: svc, Ask: a, Threads: st}, st
}

// A retry is another attempt at a question already asked, so it joins that
// question's turn instead of putting the same words in the record twice.
func TestAsk_retryJoinsTheTurnItRetries(t *testing.T) {
	deps, st := headDeps(t, &fakeAsker{tokens: []string{"antwort"}})
	ctx := context.Background()
	th, err := st.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	failed, err := st.AddQuestion(ctx, th.ID, "ba", "de", "frage", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := st.Fail(ctx, failed.ID, "kaputt"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	rec := postAsk(t, deps, fmt.Sprintf(
		`{"thread_id":%d,"question":"frage","audience":"ba","language":"de","head_message_id":%d}`,
		th.ID, failed.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}

	list, err := st.Messages(ctx, testSubject, th.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d messages, want 2 — the failure stays in the record", len(list))
	}
	if list[1].HeadMessageID != failed.ID {
		t.Errorf("retry head = %d, want the question it retries %d", list[1].HeadMessageID, failed.ID)
	}
}

// A refused head must not leave anything behind. The check runs before a
// thread is resolved, so a bad head cannot create an empty conversation on
// its way to a 403 — and a good one names its own thread, the way a card does.
func TestAsk_aRefusedHeadOpensNoThread(t *testing.T) {
	deps, st := headDeps(t, &fakeAsker{tokens: []string{"antwort"}})
	ctx := context.Background()

	rec := postAsk(t, deps, `{"question":"frage","audience":"ba","head_message_id":9999}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	list, err := st.List(ctx, testSubject)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("threads = %d, want none — a refusal writes nothing", len(list))
	}
}

// The browser names the row a retry joins, so the server checks it: a head in
// another thread would file an answer under a question it never came from.
func TestAsk_refusesAHeadFromAnotherThread(t *testing.T) {
	deps, st := headDeps(t, &fakeAsker{tokens: []string{"antwort"}})
	ctx := context.Background()
	mine, err := st.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	other, err := st.Create(ctx, testSubject, "andere frage")
	if err != nil {
		t.Fatalf("create other thread: %v", err)
	}
	elsewhere, err := st.AddQuestion(ctx, other.ID, "ba", "de", "andere frage", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}

	rec := postAsk(t, deps, fmt.Sprintf(
		`{"thread_id":%d,"question":"frage","audience":"ba","head_message_id":%d}`,
		mine.ID, elsewhere.ID))

	// Refused before the stream opens, so the status code still means
	// something: after the first SSE byte it is fixed at 200.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	list, err := st.Messages(ctx, testSubject, mine.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("wrote %d messages, want none — a refused head writes nothing", len(list))
	}
}

// The head link is what the browser groups by, so it has to be on the wire.
func TestThreadMessagesCarryTheHeadLink(t *testing.T) {
	deps, st := headDeps(t, &fakeAsker{tokens: []string{"antwort"}})
	ctx := context.Background()
	th, err := st.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	head, err := st.AddQuestion(ctx, th.ID, "ba", "de", "frage", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if err := st.Finish(ctx, head.ID, "antwort", []ask.Citation{}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := st.AddQuestion(ctx, th.ID, "dev", "de", "frage", head.ID); err != nil {
		t.Fatalf("add re-explain: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/threads/%d", th.ID), nil)
	rec := httptest.NewRecorder()
	NewServer(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var msgs []struct {
		ID            int64 `json:"id"`
		HeadMessageID int64 `json:"head_message_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].HeadMessageID != 0 {
		t.Errorf("head row head_message_id = %d, want 0", msgs[0].HeadMessageID)
	}
	if msgs[1].HeadMessageID != head.ID {
		t.Errorf("re-explain head_message_id = %d, want %d", msgs[1].HeadMessageID, head.ID)
	}
}
