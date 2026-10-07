package web

import (
	"context"
	"crypto/tls"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const shellFixture = `<meta property="og:title" content="__OG_TITLE__" />` +
	`<meta property="og:description" content="__OG_DESC__" />` +
	`<meta property="og:url" content="__OG_URL__" />` +
	`<meta property="og:image" content="__OG_IMAGE__" />`

func TestRenderShell_fillsTheSiteCardWhenNothingIsShared(t *testing.T) {
	// Given: an ordinary page, which carries no per-thread title.
	r := httptest.NewRequest(http.MethodGet, "/new", nil)
	r.Host = "rongo.example.com"

	// When
	got := string(renderShell([]byte(shellFixture), r, "", ""))

	// Then: the defaults land, and no placeholder survives — one left behind
	// would ship the literal "__OG_IMAGE__" into somebody's Slack.
	for _, want := range []string{
		`content="Rongo"`,
		`content="` + siteDesc + `"`,
		`content="http://rongo.example.com/new"`,
		`content="http://rongo.example.com/og.png"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("shell does not contain %s: %s", want, got)
		}
	}
	if strings.Contains(got, "__OG_") {
		t.Errorf("a placeholder survived: %s", got)
	}
}

func TestRenderShell_escapesTheTitleItIsGiven(t *testing.T) {
	// Given: a thread title is whatever its owner asked, and it goes into an
	// HTML attribute.
	r := httptest.NewRequest(http.MethodGet, "/share/kd8Qw1rZ", nil)

	// When
	got := string(renderShell([]byte(shellFixture), r, `Why "x" > y & <script>`, shareDesc))

	// Then
	if strings.Contains(got, "<script>") {
		t.Errorf("title was not escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("escaped title missing: %s", got)
	}
}

// Behind a TLS-terminating proxy the process only ever sees plain HTTP on an
// internal address, which is why the origin is read off the forwarded headers
// rather than out of the listener. Getting this wrong ships an http:// image
// URL that Slack refuses to load on an https page.
func TestOrigin_prefersWhatTheProxySays(t *testing.T) {
	tests := []struct {
		name  string
		build func(*http.Request)
		want  string
	}{
		{
			name:  "plain http, no proxy",
			build: func(r *http.Request) { r.Host = "localhost:8080" },
			want:  "http://localhost:8080",
		},
		{
			name: "TLS terminated in the process itself",
			build: func(r *http.Request) {
				r.Host = "rongo.example.com"
				r.TLS = &tls.ConnectionState{}
			},
			want: "https://rongo.example.com",
		},
		{
			name: "proxy sets both",
			build: func(r *http.Request) {
				r.Host = "10.0.0.4:8080"
				r.Header.Set("X-Forwarded-Proto", "https")
				r.Header.Set("X-Forwarded-Host", "rongo.example.com")
			},
			want: "https://rongo.example.com",
		},
		{
			name: "a chain of proxies lists the hop nearest the reader first",
			build: func(r *http.Request) {
				r.Host = "10.0.0.4:8080"
				r.Header.Set("X-Forwarded-Proto", "https, http")
				r.Header.Set("X-Forwarded-Host", "rongo.example.com, 10.0.0.4")
			},
			want: "https://rongo.example.com",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.TLS = nil
			tc.build(r)

			if got := origin(r); got != tc.want {
				t.Errorf("origin = %q, want %q", got, tc.want)
			}
		})
	}
}

// liveToken is a share token of the shape threads.newToken mints.
const liveToken = "kd8Qw1rZx3Yv9pLmN0aB_c"

// builtFS stands in for a tree someone has run `make fe-build` over. The real
// dist/ is gitignored and only exists after that build, so CI's Go job never
// has one — a test that read the embedded copy would skip there and assert
// nothing.
func builtFS() fs.FS {
	return fstest.MapFS{
		"index.html":             {Data: []byte(shellFixture)},
		"icon.svg":               {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
		"icon-192.png":           {Data: []byte("\x89PNG\r\n\x1a\n")},
		"apple-touch-icon.png":   {Data: []byte("\x89PNG\r\n\x1a\n")},
		"og.png":                 {Data: []byte("\x89PNG\r\n\x1a\n")},
		"manifest.webmanifest":   {Data: []byte(`{"name":"Rongo"}`)},
		"assets/index-abc123.js": {Data: []byte("export {}")},
	}
}

// The share title is the whole reason the shell is rendered rather than served
// as a file, so check it end to end through the handler.
func TestHandler_putsTheShareTitleInTheCard(t *testing.T) {
	// Given: a lookup that knows one token and nothing else.
	h := handler(builtFS(), func(_ context.Context, token string) (string, bool, error) {
		if token == liveToken {
			return "Why can an order be cancelled twice?", true, nil
		}
		return "", false, nil
	})

	tests := []struct {
		name      string
		path      string
		wantTitle string
		wantDesc  string
		noindex   bool
	}{
		{
			name:      "a live link unfurls as its own question",
			path:      "/share/" + liveToken,
			wantTitle: "Why can an order be cancelled twice?",
			wantDesc:  shareDesc,
			noindex:   true,
		},
		{
			name:      "an ordinary page is not marked noindex",
			path:      "/new",
			wantTitle: siteTitle,
			wantDesc:  siteDesc,
			noindex:   false,
		},
		{
			// The root's trimmed name is "", which no fs.FS stats, so it never
			// reaches the file server and is substituted like any other route.
			name:      "the root is substituted too",
			path:      "/",
			wantTitle: siteTitle,
			wantDesc:  siteDesc,
			noindex:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// When
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `og:title" content="`+tc.wantTitle+`"`) {
				t.Errorf("og:title is not %q", tc.wantTitle)
			}
			if !strings.Contains(body, `og:description" content="`+tc.wantDesc+`"`) {
				t.Errorf("og:description is not %q", tc.wantDesc)
			}
			if strings.Contains(body, "__OG_") {
				t.Error("a placeholder survived into the served shell")
			}
			if got := rec.Header().Get("X-Robots-Tag"); (got != "") != tc.noindex {
				t.Errorf("X-Robots-Tag = %q, noindex want %v", got, tc.noindex)
			}
			// The card is built from the request headers, so it must not be
			// cached by anything in front keyed on the path alone.
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// A crawler reads the shell, so the icon and manifest it points at have to be
// served, and served as themselves. The manifest is the one that bites: Go's
// MIME table has no .webmanifest, and Chrome rejects a manifest that arrives
// as text/plain by silently dropping the PWA icons and theme colour.
func TestHandler_servesTheIconsAndTheManifest(t *testing.T) {
	h := handler(builtFS(), nil)

	tests := []struct{ path, wantType string }{
		{"/icon.svg", "image/svg+xml"},
		{"/icon-192.png", "image/png"},
		{"/apple-touch-icon.png", "image/png"},
		{"/og.png", "image/png"},
		{"/manifest.webmanifest", "application/manifest+json"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantType) {
				t.Errorf("Content-Type = %q, want %q", ct, tc.wantType)
			}
		})
	}
}

// There is no favicon.ico and no /favicon.svg; the tab icon is /icon.svg. Both
// stay hard 404s rather than falling through to the shell, which would answer
// text/html to something asking for an image.
func TestHandler_hasNoFavicon(t *testing.T) {
	h := handler(builtFS(), nil)

	for _, path := range []string{"/favicon.ico", "/favicon.svg"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
		})
	}
}

// A revoked or invented token is a real 404, the same answer the public API
// gives for it, and still kept out of a search index. A 200 shell would tell
// a crawler and a monitor the link works. The body is still the app, so the
// reader sees its own "no longer available" page rather than bare text, and
// nothing in front caches it: re-sharing a revoked thread returns the SAME
// token, and a cached 404 would outlive that.
func TestShellDeadShareLinkIs404(t *testing.T) {
	dead := func(context.Context, string) (string, bool, error) { return "", false, nil }

	t.Run("built", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler(builtFS(), dead).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/"+liveToken, nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if rec.Header().Get("X-Robots-Tag") == "" {
			t.Error("a dead share link is indexable")
		}
		if !strings.Contains(rec.Body.String(), `og:title" content="`+siteTitle+`"`) {
			t.Errorf("a dead share link was not served the shell with the site card: %q", rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		if rec.Header().Get("Vary") == "" {
			t.Error("no Vary on a shell built from the request")
		}
		if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
			t.Errorf("Content-Length = %q, body is %d bytes", got, rec.Body.Len())
		}
	})

	t.Run("placeholder", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler(fstest.MapFS{}, dead).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/"+liveToken, nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if rec.Header().Get("X-Robots-Tag") == "" {
			t.Error("a dead share link is indexable")
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("a dead share link may be cached")
		}
	})
}

// Only what the store mints is a share link; anything else never reaches the
// record. The placeholder build answers the same set, and marks a share link
// noindex like the shell does.
func TestShellShareRouteNeedsTokenShape(t *testing.T) {
	asked := 0
	lookup := func(context.Context, string) (string, bool, error) {
		asked++
		return "A title", true, nil
	}
	for name, fsys := range map[string]fs.FS{"built": builtFS(), "placeholder": fstest.MapFS{}} {
		h := handler(fsys, lookup)
		for _, path := range []string{"/share/nope", "/share/" + liveToken + "x", "/share/kd8Qw1rZx3Yv9pLmN0aB.c", "/share/" + liveToken + "/x"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d, want 404", name, path, rec.Code)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/"+liveToken, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: a token of the right shape: status = %d, want 200", name, rec.Code)
		}
		if rec.Header().Get("X-Robots-Tag") == "" {
			t.Errorf("%s: a share link is indexable", name)
		}
	}
	if asked != 2 {
		t.Errorf("the record was asked %d times, want twice (one well-formed token per build)", asked)
	}
}

// A record that cannot answer is not a revocation: the shell is served with
// the site card, and the SPA asks the API itself.
func TestShellShareTitleErrorStillServesShell(t *testing.T) {
	h := handler(builtFS(), func(context.Context, string) (string, bool, error) {
		return "", false, errors.New("database is locked")
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/"+liveToken, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `og:title" content="`+siteTitle+`"`) {
		t.Error("an outage did not fall back to the site card")
	}
	if rec.Header().Get("X-Robots-Tag") == "" {
		t.Error("a share link is indexable")
	}
}
