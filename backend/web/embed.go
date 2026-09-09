// Package web serves the embedded single-page application.
package web

import (
	"context"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// Go's MIME table has no .webmanifest entry, so http.FileServer would answer
// text/plain for it. Chrome rejects a manifest served that way and silently
// drops the PWA icons and theme colour — no console error, nothing in the
// network tab beyond a 200.
func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

//go:embed all:dist
var distFS embed.FS

// placeholderHTML ships separately from dist/. `vite build` runs with
// emptyOutDir: false (see ui/vite.config.ts) so the tracked dist/.gitkeep
// survives; the Makefile's fe-build target does `rm -rf dist/assets` instead
// to remove stale hashed assets a build no longer produces. Embedding the
// placeholder inside dist would mean a build permanently overwrites the
// tracked placeholder with the built shell the first time anyone runs
// `make build`.
//
//go:embed placeholder.html
var placeholderHTML []byte

// HasBuiltIndex reports whether the embedded dist/ contains a real built
// index.html (as opposed to just the tracked .gitkeep). Callers — notably
// tests — use this to know which of the two legitimate states (built vs.
// fresh clone) the binary is actually serving, without duplicating the
// embed.FS lookup.
func HasBuiltIndex() bool {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, "index.html")
	return err == nil
}

// isRoute reports whether a path is one the SPA actually has a page for. It is
// the same set ui/src/routing.ts parses, kept here so an address that leads
// nowhere gets a real 404 instead of 200 and a shell that quietly renders the
// unasked question — a soft 404 tells a reader, a crawler and a monitor that
// the link worked.
//
// A thread address is checked by SHAPE only: 22 URL-safe characters, what the
// store mints. Whether that thread exists is a question this handler cannot
// answer — it has no session, and the only thing it may ask the record is a
// share link's title — and answering it here would tell anyone, signed in or
// not, which addresses are real. The app asks the API and says so itself.
func isRoute(path string) bool {
	switch path {
	case "/", "/new", "/projects", "/shared":
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/share/"); ok {
		return rest != ""
	}
	if rest, ok := strings.CutPrefix(path, "/thread/"); ok {
		if len(rest) != threadAddressLen {
			return false
		}
		for _, c := range rest {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
		return true
	}
	return false
}

// threadAddressLen is 16 random bytes in base64url without padding, which is
// what threads.newToken mints.
const threadAddressLen = 22

// Handler serves the built SPA. A path the SPA has a page for falls back to
// index.html so the client-side router can take over; anything else is a 404.
// /api paths are excluded first so a typo in an endpoint stays a 404 instead
// of silently returning HTML.
// /assets/ is excluded the same way: those are content-hashed files vite
// emits, so a missing one means a browser holding a stale cached index.html
// is asking for a chunk that no longer exists after a redeploy. Falling back
// to the SPA shell there would return 200 text/html for a JS module request,
// which reads to the browser as a broken script instead of the 404 a
// reload-on-stale-chunk heuristic can act on. If the SPA has never been
// built, dist/index.html is absent (only .gitkeep is tracked there) and the
// handler serves the placeholder instead.
func Handler() http.Handler { return HandlerWithShareTitles(nil) }

// ShareTitle answers the one question the shell asks about a share link: what
// the shared thread is called. It reports false for a token that is unknown or
// revoked, exactly as GET /api/shares/{token} already answers 404 for those —
// so this tells a crawler nothing that endpoint does not.
type ShareTitle func(ctx context.Context, token string) (string, bool)

// HandlerWithShareTitles is Handler with the link-preview title for /share/
// wired up. Crawlers do not run JavaScript, so a share link unfurls with
// whatever the served HTML says; SharePage sets document.title long after
// Slack has read the page and left.
//
// Passing nil serves the site-wide card everywhere, which is what Handler
// does and what a binary without a thread record has to do anyway.
func HandlerWithShareTitles(shareTitle ShareTitle) http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist directory missing from embed: " + err.Error())
	}
	return handler(sub, shareTitle)
}

// handler is HandlerWithShareTitles over any file system, so a test can serve
// a shell of its own. The embedded dist/ is only built by `make fe-build` and
// is gitignored, so CI runs the Go job against a tree that has none — a test
// that needed the real one would skip there and assert nothing.
func handler(sub fs.FS, shareTitle ShareTitle) http.Handler {
	files := http.FileServer(http.FS(sub))
	_, builtIndexErr := fs.Stat(sub, "index.html")
	// Read once. The shell is a few kilobytes and every SPA route serves it.
	// A shell that stats but cannot be read is the same situation as no build
	// at all, so it takes the placeholder path rather than serving nothing.
	shell, shellErr := fs.ReadFile(sub, "index.html")
	if builtIndexErr == nil && shellErr != nil {
		builtIndexErr = shellErr
	}

	// serveShell writes the SPA shell with its link-preview placeholders
	// filled in. A share link carries the thread's own title and a noindex
	// header, matching what the public API sends for the same token: unfurl
	// it, do not put it in a search index.
	serveShell := func(w http.ResponseWriter, r *http.Request) {
		var title, desc string
		if token, ok := strings.CutPrefix(r.URL.Path, "/share/"); ok && token != "" {
			if shareTitle != nil {
				if t, found := shareTitle(r.Context(), token); found {
					// Both together, or neither: a revoked link described as
					// "a shared thread, frozen where it was shared" under the
					// bare site title is a card for something that is gone.
					title, desc = t, shareDesc
				}
			}
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		body := renderShell(shell, r, title, desc)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		// The card is built from the request, so a cache in front keyed on
		// path alone would serve one reader's host and title to the next.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Vary", "X-Forwarded-Host, X-Forwarded-Proto")
		_, _ = w.Write(body)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "/api" (no trailing slash) must also be excluded, not just "/api/" —
		// a prefix-only check on "/api/" lets the bare form fall through to
		// the SPA shell instead of a 404.
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if builtIndexErr != nil {
			// Nothing under dist/ is built yet, so /assets/ can't contain a
			// real file either; a missing built dist and a missing asset
			// both mean "not found", not the placeholder.
			if strings.HasPrefix(r.URL.Path, "/assets/") || !isRoute(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(placeholderHTML)
			return
		}
		// "/" comes through here too, and lands on the shell: the trimmed name
		// is "", which no fs.FS will stat, so the root never reaches the file
		// server. "/index.html" does, and the file server answers it with its
		// usual 301 to "./" rather than the raw shell.
		if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
			if strings.HasPrefix(r.URL.Path, "/assets/") || !isRoute(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			serveShell(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
