package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz_returnsOK(t *testing.T) {
	// Given
	srv := NewServer(Deps{})

	// When
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestUnknownRoute_returns404(t *testing.T) {
	srv := NewServer(Deps{})

	req := httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
