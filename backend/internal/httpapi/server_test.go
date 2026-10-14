package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/version"
)

// probe is a request to a probe path from loopback: kit answers the probes
// for a direct local caller only.
func probe(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:40000"
	return req
}

func TestHealthz_returnsOK(t *testing.T) {
	// Given
	srv := NewServer(Deps{})

	// When
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, probe("/healthz"))

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "ok\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// Readiness runs the deployment's checks; liveness never does, so a database
// outage takes the replica out of rotation instead of restarting it.
func TestReadyz_failsOnAFailingCheck(t *testing.T) {
	// Given
	srv := NewServer(Deps{Ready: []httpx.Check{{Name: "database", Run: func(context.Context) error {
		return errors.New("database is locked")
	}}}})

	// When
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, probe("/readyz"))
	live := httptest.NewRecorder()
	srv.ServeHTTP(live, probe("/healthz"))

	// Then
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"check":"database"`) {
		t.Errorf("readyz = %d %s, want 503 naming the check", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "locked") {
		t.Errorf("readyz body %q carries the error, which goes to the log only", rec.Body.String())
	}
	if live.Code != http.StatusOK {
		t.Errorf("healthz = %d, want 200 whatever readiness says", live.Code)
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
		"a sibling subdomain":                    {"Sec-Fetch-Site": "same-site"},
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

// Behind a proxy that rewrites Host and forwards no X-Forwarded-Host, every
// write from an older browser is refused on Origin. The log says which hosts
// disagreed, or the operator sees only 403s.
func TestCrossSiteOriginMismatchIsLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Origin", "https://rongo.example")
	rec := httptest.NewRecorder()
	NewServer(Deps{Auth: devAuth(t)}).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	line := buf.String()
	if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "rongo.example") || !strings.Contains(line, "example.com") {
		t.Errorf("refusal not logged with both hosts: %s", line)
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
