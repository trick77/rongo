package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/rongo/web"
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
	// dist/index.html is gitignored, so it can never be committed broken —
	// but it can be legitimately absent (fresh clone, no `make fe-build` yet)
	// or present (built). Both are valid states with their own contract:
	// a build must keep the mount point, a fresh clone must serve the
	// placeholder rather than something that merely looks like HTML.
	if web.HasBuiltIndex() {
		if !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Errorf("body does not look like the built SPA shell: %q", rec.Body.String())
		}
	} else {
		if !strings.Contains(rec.Body.String(), "SPA not built") {
			t.Errorf("body does not look like the placeholder: %q", rec.Body.String())
		}
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

func TestSPA_missingAssetIs404(t *testing.T) {
	// Given: /assets/ holds content-hashed files. A browser holding a stale
	// cached index.html after a redeploy asks for a chunk that no longer
	// exists; it must get a 404, not the SPA shell served as text/html.
	srv := NewServer(Deps{})

	req := httptest.NewRequest(http.MethodGet, "/assets/nope.js", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
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
