package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
