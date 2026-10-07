package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/rongo/internal/version"
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

// The composer's footer names the build that answered, and this is the request
// it reads it from: the session check the UI already makes before it renders.
func TestMe_carriesTheBuildVersion(t *testing.T) {
	// Given: a server, and a stamped binary
	srv := newTestServer(t)
	prev := version.Version
	t.Cleanup(func() { version.Version = prev })
	version.Version = "9.9.9"

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Email   string `json:"email"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != "9.9.9" {
		t.Errorf("version = %q, want %q", body.Version, "9.9.9")
	}
	if body.Email == "" {
		t.Error("email is empty; the session fields must survive the addition")
	}
}

// Dev and proxy mode admit a request with no cookie at all, so SameSite does
// not stop a cross-site form from posting to a bodiless action. The gate does.
func TestCrossSitePostIsRefused(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"fetch metadata says cross-site":         {"Sec-Fetch-Site": "cross-site"},
		"origin names another host":              {"Origin": "https://evil.example"},
		"opaque origin":                          {"Origin": "null"},
		"cross-site wins over a matching origin": {"Sec-Fetch-Site": "cross-site", "Origin": "http://example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			NewServer(Deps{Auth: devAuth(t)}).ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
		})
	}
}

func TestSameOriginPostPasses(t *testing.T) {
	for name, tc := range map[string]struct {
		method  string
		headers map[string]string
	}{
		"the SPA's own fetch":              {http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com"}},
		"a same-origin origin alone":       {http.MethodPost, map[string]string{"Origin": "http://example.com"}},
		"origin of the proxy's host":       {http.MethodPost, map[string]string{"Origin": "https://rongo.example", "X-Forwarded-Host": "rongo.example"}},
		"a script with no browser headers": {http.MethodPost, nil},
		"a cross-site GET":                 {http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}},
	} {
		t.Run(name, func(t *testing.T) {
			path := "/api/auth/logout"
			if tc.method == http.MethodGet {
				path = "/api/me"
			}
			req := httptest.NewRequest(tc.method, path, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			NewServer(Deps{Auth: devAuth(t)}).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
			}
		})
	}
}
