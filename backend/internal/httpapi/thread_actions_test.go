package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
)

// threadActions wires a dev-auth server over a real thread store and hands
// back both, so a test can act through HTTP and read the record directly.
// Both readers are seeded: a thread references its owner, and Create fails
// against a subject with no user row.
func threadActions(t *testing.T) (*Server, *threads.Store) {
	t.Helper()
	db := askDB(t)
	svc := auth.NewService(db, "dev", "")
	for _, subject := range []string{testSubject, otherSubject} {
		if _, err := svc.UpsertUser(subject, subject+"@example.invalid", true); err != nil {
			t.Fatalf("seed user %q: %v", subject, err)
		}
	}
	st := threads.NewStore(db)
	return NewServer(Deps{Auth: svc, Threads: st}), st
}

// otherSubject is anyone who is not the reader making the request.
const otherSubject = "someone-else"

// act sends one thread action, on the shared `do` from auth_handlers_test.go.
func act(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	return do(srv, httptest.NewRequest(method, path, strings.NewReader(body)))
}

func TestDeleteThread_takesItOffTheRail(t *testing.T) {
	// Given a thread of this reader's
	ctx := context.Background()
	srv, st := threadActions(t)
	th, err := st.Create(ctx, testSubject, "How is sign-in done?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// When
	rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d", th.ID), "")

	// Then
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	list, _ := st.List(ctx, testSubject)
	if len(list) != 0 {
		t.Errorf("threads = %+v, want the list empty", list)
	}
}

func TestDeleteThread_anotherReadersThreadIsNotFound(t *testing.T) {
	// Someone else's thread reads exactly like one that never existed: the
	// answer must not tell a caller which threads are out there.
	ctx := context.Background()
	srv, st := threadActions(t)
	th, err := st.Create(ctx, otherSubject, "How is sign-in done?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d", th.ID), "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	list, _ := st.List(ctx, otherSubject)
	if len(list) != 1 {
		t.Errorf("threads = %+v, want the other reader's thread standing", list)
	}
}

func TestDeleteThread_aMalformedIDIsRejected(t *testing.T) {
	srv, _ := threadActions(t)

	rec := act(srv, http.MethodDelete, "/api/threads/nope", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRenameThread_writesTheTypedTitle(t *testing.T) {
	// Given
	ctx := context.Background()
	srv, st := threadActions(t)
	th, err := st.Create(ctx, testSubject, "How is sign-in done?")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// When
	rec := act(srv, http.MethodPatch, fmt.Sprintf("/api/threads/%d", th.ID), `{"title":"Sign-in, the whole path"}`)

	// Then
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	list, _ := st.List(ctx, testSubject)
	if list[0].Title != "Sign-in, the whole path" {
		t.Errorf("title = %q, want the typed one", list[0].Title)
	}
}

func TestRenameThread_refusesAnEmptyTitle(t *testing.T) {
	ctx := context.Background()
	srv, st := threadActions(t)
	th, _ := st.Create(ctx, testSubject, "How is sign-in done?")

	for _, body := range []string{`{"title":"   "}`, `{}`, `not json`} {
		rec := act(srv, http.MethodPatch, fmt.Sprintf("/api/threads/%d", th.ID), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
	list, _ := st.List(ctx, testSubject)
	if !strings.HasPrefix(list[0].Title, "How is sign-in") {
		t.Errorf("title = %q, want the placeholder untouched", list[0].Title)
	}
}

func TestRenameThread_refusesATitleLongerThanTheRailCanHold(t *testing.T) {
	// A title is a line of text. One that is not would be stored, then shipped
	// with the list on every load of the rail.
	ctx := context.Background()
	srv, st := threadActions(t)
	th, _ := st.Create(ctx, testSubject, "How is sign-in done?")

	rec := act(srv, http.MethodPatch, fmt.Sprintf("/api/threads/%d", th.ID),
		`{"title":"`+strings.Repeat("a", 49)+`"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	list, _ := st.List(ctx, testSubject)
	if !strings.HasPrefix(list[0].Title, "How is sign-in") {
		t.Errorf("title = %q, want the placeholder untouched", list[0].Title)
	}
}

func TestRenameThread_anotherReadersThreadIsNotFound(t *testing.T) {
	ctx := context.Background()
	srv, st := threadActions(t)
	th, _ := st.Create(ctx, otherSubject, "How is sign-in done?")

	rec := act(srv, http.MethodPatch, fmt.Sprintf("/api/threads/%d", th.ID), `{"title":"Mine now"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestThreadActions_withoutAStoreAnswer503(t *testing.T) {
	db := askDB(t)
	srv := NewServer(Deps{Auth: auth.NewService(db, "dev", "")})

	for _, c := range []struct{ method, body string }{
		{http.MethodDelete, ""},
		{http.MethodPatch, `{"title":"x"}`},
	} {
		rec := act(srv, c.method, "/api/threads/1", c.body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", c.method, rec.Code)
		}
	}
}

// TestDeleteThread_stopsTheAnswerBeingWrittenIntoIt is the delete that lands
// mid-answer. Without the cancel the turn runs its model calls to completion,
// and is paid for, against a thread whose rows the cascade has already taken.
func TestDeleteThread_stopsTheAnswerBeingWrittenIntoIt(t *testing.T) {
	// Given a turn that deletes its own thread while it is being answered
	var turnCtx context.Context
	srv, st := newTestServerWithStore(t, func(f *fakeAsker) {
		f.tokens = []string{"The ", "answer."}
	})
	asker := srv.deps.Ask.(*fakeAsker)
	asker.during = func(ctx context.Context) {
		turnCtx = ctx
		list, err := st.List(ctx, testSubject)
		if err != nil || len(list) != 1 {
			t.Fatalf("List = %+v, %v; want the one thread being answered", list, err)
		}
		rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d", list[0].ID), "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete status = %d (%s), want 204", rec.Code, rec.Body.String())
		}
	}

	// When
	doSSE(t, srv, "/api/ask", `{"question":"How is sign-in done?"}`)

	// Then the turn was cut short, and says why
	if turnCtx == nil {
		t.Fatal("the turn never ran")
	}
	if turnCtx.Err() == nil {
		t.Error("the turn is still running after its thread was deleted")
	}
	if !errors.Is(context.Cause(turnCtx), errThreadDeleted) {
		t.Errorf("cause = %v, want errThreadDeleted", context.Cause(turnCtx))
	}
	// And nothing was written back into it: the answer that was in flight has
	// nowhere to land, which is the point of deleting a thread.
	list, err := st.List(context.Background(), testSubject)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("threads = %+v, want the list empty", list)
	}
}

// TestDeleteThread_nothingIsWrittenBackAfterwards covers the writes that run on
// contexts the cancel does not reach: the record's, which outlives the request
// on purpose, and the title's, which is forked before the turn and settles the
// row from its own goroutine. None of them may put a row back.
func TestDeleteThread_nothingIsWrittenBackAfterwards(t *testing.T) {
	// Given a turn with a title in flight, whose thread is deleted under it
	deps, st := askDeps(t, &fakeAsker{tokens: []string{"It runs through a job."}})
	deps.Titler = func(context.Context, string, ask.Language) string { return "Shipping, end to end" }
	srv := NewServer(deps)
	deps.Ask.(*fakeAsker).during = func(ctx context.Context) {
		list, _ := st.List(ctx, testSubject)
		if len(list) != 1 {
			t.Fatalf("threads = %+v, want the one being answered", list)
		}
		if rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d", list[0].ID), ""); rec.Code != http.StatusNoContent {
			t.Fatalf("delete status = %d, want 204", rec.Code)
		}
	}

	// When
	do(srv, httptest.NewRequest(http.MethodPost, "/api/ask", strings.NewReader(
		`{"question":"How does shipping work?","audience":"ba"}`)))

	// Then
	list, err := st.List(context.Background(), testSubject)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("threads = %+v, want the list empty", list)
	}
}

// TestDeleteThread_stopsAReexplainToo: a re-explain is a paid turn like any
// other, and the thread it re-answers can go under it the same way.
func TestDeleteThread_stopsAReexplainToo(t *testing.T) {
	// Given a re-explain whose thread is deleted while it runs
	var turnCtx context.Context
	srv, store, db := newTestServerWithDB(t, withAskerReexplaining())
	msgID := seedAnsweredMessageWithSources(t, store, db)
	asker := srv.deps.Ask.(*fakeAsker)
	asker.during = func(ctx context.Context) {
		turnCtx = ctx
		list, _ := store.List(ctx, testSubject)
		if len(list) != 1 {
			t.Fatalf("threads = %+v, want the one being re-explained", list)
		}
		if rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d", list[0].ID), ""); rec.Code != http.StatusNoContent {
			t.Fatalf("delete status = %d, want 204", rec.Code)
		}
	}

	// When
	doSSE(t, srv, fmt.Sprintf("/api/messages/%d/reexplain", msgID), `{"audience":"dev"}`)

	// Then
	if turnCtx == nil {
		t.Fatal("the re-explain never ran")
	}
	if !errors.Is(context.Cause(turnCtx), errThreadDeleted) {
		t.Errorf("cause = %v, want errThreadDeleted", context.Cause(turnCtx))
	}
	list, _ := store.List(context.Background(), testSubject)
	if len(list) != 0 {
		t.Errorf("threads = %+v, want the list empty", list)
	}
}

// TestDeleteThread_someoneElsesTurnIsLeftAlone is the other half: the delete is
// the ownership check, so a 404 must not reach into a turn it does not own.
func TestDeleteThread_someoneElsesTurnIsLeftAlone(t *testing.T) {
	// Given a turn of this reader's, and a delete for another reader's thread
	var turnCtx context.Context
	srv, st := newTestServerWithStore(t, func(f *fakeAsker) {
		f.tokens = []string{"The ", "answer."}
	})
	other, err := st.Create(context.Background(), "someone-else", "Theirs.")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	asker := srv.deps.Ask.(*fakeAsker)
	asker.during = func(ctx context.Context) {
		turnCtx = ctx
		rec := act(srv, http.MethodDelete, fmt.Sprintf("/api/threads/%d", other.ID), "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("delete status = %d, want 404", rec.Code)
		}
	}

	// When
	doSSE(t, srv, "/api/ask", `{"question":"How is sign-in done?"}`)

	// Then
	if turnCtx == nil {
		t.Fatal("the turn never ran")
	}
	if errors.Is(context.Cause(turnCtx), errThreadDeleted) {
		t.Error("a 404 delete cut a turn it does not own")
	}
	list, _ := st.List(context.Background(), testSubject)
	if len(list) != 1 {
		t.Fatalf("threads = %+v, want the answered thread standing", list)
	}
	msgs, _ := st.Messages(context.Background(), testSubject, list[0].ID)
	if len(msgs) != 1 || msgs[0].Answer != "The answer." {
		t.Errorf("messages = %+v, want the answer recorded", msgs)
	}
}
