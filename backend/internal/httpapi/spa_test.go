package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPA_servesIndexAtRoot(t *testing.T) {
	// Given
	srv := NewServer(Deps{})

	// When
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	// A missing or placeholder build still contains "<!doctype html>", so that
	// alone passes even when the SPA was never built. id="root" only appears
	// in the real built index.html, so this fails loudly on a missing build.
	if !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Errorf("body does not look like the built SPA shell: %q", rec.Body.String())
	}
}

func TestSPA_fallsBackForClientRoutes(t *testing.T) {
	// Given: the SPA owns its own routing, so an unknown non-API path must
	// return index.html rather than 404.
	srv := NewServer(Deps{})

	req := httptest.NewRequest(http.MethodGet, "/threads/42", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestSPA_doesNotSwallowAPIRoutes(t *testing.T) {
	// Given: an unknown /api path — with or without a trailing segment — must
	// stay a 404, never the SPA shell. The bare "/api" form is the one a
	// prefix-only check on "/api/" misses.
	for _, path := range []string{"/api/nope", "/api"} {
		t.Run(path, func(t *testing.T) {
			srv := NewServer(Deps{})

			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
		})
	}
}
