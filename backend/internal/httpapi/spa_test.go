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
	// Given: the SPA owns its own routing, so the paths it has a page for must
	// return index.html rather than 404 — including a thread address, which is
	// checked by shape here because this handler has no session and no
	// database, and answering "no such thread" would tell anyone which
	// addresses are real.
	srv := NewServer(Deps{})

	for _, path := range []string{"/new", "/projects", "/shared", "/thread/v76BBy2b1nMYOFl2Lnm9JQ", "/share/kd8Qw1rZ"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", path, rec.Code, http.StatusOK)
		}
	}
}

// A path the app has no page for is a real 404, not 200 and a shell that
// quietly renders the unasked question. A soft 404 tells the reader, a crawler
// and a monitor that the link worked.
//
// "/thread/19" is on this list for good: threads were addressed by row number
// for one release, and those URLs are not redirected — a redirect would keep
// the counter reachable for ever.
func TestSPA_aPathTheAppHasNoPageForIsNotFound(t *testing.T) {
	srv := NewServer(Deps{})

	for _, path := range []string{
		"/threads/42", "/nope", "/thread/", "/thread/19", "/thread/abc",
		"/thread/v76BBy2b1nMYOFl2Lnm9JQx", "/thread/v76BBy2b1nMYOFl2Lnm9J.", "/share/",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
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
