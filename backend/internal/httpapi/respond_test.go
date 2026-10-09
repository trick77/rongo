package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/threads"
)

func TestDecodeJSON_malformedBodyIs400(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":`))
	var v struct{ Title string }

	if decodeJSON(rec, req, &v) {
		t.Fatal("decodeJSON = true on a malformed body")
	}
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != "malformed request" {
		t.Errorf("got %d %q, want 400 malformed request", rec.Code, rec.Body.String())
	}
}

func TestDecodeJSON_fillsTheValueAndWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"x"}`))
	var v struct{ Title string }

	if !decodeJSON(rec, req, &v) || v.Title != "x" {
		t.Fatalf("decodeJSON = false or v = %+v", v)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("wrote %q on a good body", rec.Body.String())
	}
}

func TestReader_noThreadsIs503(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), auth.User{Subject: "someone"}))

	if _, ok := s.reader(rec, req); ok {
		t.Fatal("reader = ok with no thread store")
	}
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != "threads unavailable" {
		t.Errorf("got %d %q, want 503 threads unavailable", rec.Code, rec.Body.String())
	}
}

func TestReader_noUserIs401(t *testing.T) {
	s := &Server{deps: Deps{Threads: &threads.Store{}}}
	rec := httptest.NewRecorder()

	if _, ok := s.reader(rec, httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatal("reader = ok with no user")
	}
	if rec.Code != http.StatusUnauthorized || strings.TrimSpace(rec.Body.String()) != "unauthorized" {
		t.Errorf("got %d %q, want 401 unauthorized", rec.Code, rec.Body.String())
	}
}

func TestReader_returnsTheSignedInUser(t *testing.T) {
	s := &Server{deps: Deps{Threads: &threads.Store{}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), auth.User{Subject: "someone"}))

	u, ok := s.reader(rec, req)
	if !ok || u.Subject != "someone" || rec.Body.Len() != 0 {
		t.Errorf("reader = %+v %v, body %q", u, ok, rec.Body.String())
	}
}

func TestActionOutcome(t *testing.T) {
	cases := []struct {
		name   string
		found  bool
		err    error
		wantOK bool
		status int
		body   string
	}{
		{"store error is 500", false, errors.New("disk is full"), false, http.StatusInternalServerError, "internal server error"},
		{"no row is the given 404", false, nil, false, http.StatusNotFound, "no such share"},
		{"a row writes nothing", true, nil, true, http.StatusOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ok := actionOutcome(rec, c.found, c.err, "revoke share failed", "no such share")
			if ok != c.wantOK || rec.Code != c.status || strings.TrimSpace(rec.Body.String()) != c.body {
				t.Errorf("got %v %d %q, want %v %d %q", ok, rec.Code, rec.Body.String(), c.wantOK, c.status, c.body)
			}
		})
	}
}

func TestThreadAction_noThreadsIs503BeforeTheStoreIsAsked(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/threads/x/share", nil)
	called := false

	s.threadAction(rec, req, "revoke share failed", "no such share", func(context.Context, string, int64) (bool, error) {
		called = true
		return true, nil
	})

	if called || rec.Code != http.StatusServiceUnavailable {
		t.Errorf("called=%v code=%d, want the store left alone and 503", called, rec.Code)
	}
}
