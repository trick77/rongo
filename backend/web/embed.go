// Package web serves the built SPA through kit's spa.Handler, with rongo's
// two additions: an allowlist of the pages the client router has, so a path
// nowhere gets a real 404 instead of a shell that quietly renders the
// unasked question, and the share link's title in the HTML for link
// previews, since a crawler does not run JavaScript.
package web

import (
	"context"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/trick77/ship/kit/spa"
)

func init() {
	// Go's mime table has no entry for the manifest; without one the file
	// goes out as text/plain and the browser ignores it.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

//go:embed all:dist
var distFS embed.FS

// placeholderHTML is what an unbuilt dist serves on an app route: a dev
// checkout that has not run the UI build yet.
//
//go:embed placeholder.html
var placeholderHTML []byte

// HasBuiltIndex reports whether the embedded dist holds a built shell.
func HasBuiltIndex() bool {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return false
	}
	return spa.Built(sub)
}

// isRoute is the client router's page list: what gets the shell. Everything
// else is a real 404, so a typo in a link does not look like an empty page.
func isRoute(path string) bool {
	switch path {
	case "/", "/new", "/threads", "/projects", "/shared", "/memory":
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/share/"); ok {
		return isAddress(rest)
	}
	if rest, ok := strings.CutPrefix(path, "/thread/"); ok {
		return isAddress(rest)
	}
	return false
}

// isAddress reports whether s has the shape threads.newToken mints. Thread
// addresses and share tokens are both minted there.
func isAddress(s string) bool {
	if len(s) != addressLen {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// addressLen is the length of the base64url string threads.newToken mints.
const addressLen = 22

// ShareTitle answers the one question the shell asks about a share link: what
// the shared thread is called. It reports found == false for a token that is
// unknown or revoked, and the page answers 404, exactly as GET
// /api/shares/{token} already does for those — so this tells a crawler nothing
// the API would not.
type ShareTitle func(ctx context.Context, token string) (title string, found bool, err error)

// Handler serves the built SPA with the share link's title in its card.
func Handler(shareTitle ShareTitle) http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist directory missing from embed: " + err.Error())
	}
	return handler(sub, shareTitle)
}

// handler is kit's spa.Handler over dist: the router's page list as Routes,
// the site card rendered into the shell per request (Index), and the share
// pages answered whole in Gate, where the status and the headers can be
// rongo's. A share page is looked up once; a crawler reads the title off the
// HTML, a reader's browser renders it again from the API.
func handler(dist fs.FS, shareTitle ShareTitle) http.Handler {
	shell, shellErr := fs.ReadFile(dist, "index.html")
	sharePage := func(w http.ResponseWriter, r *http.Request) bool {
		token, ok := strings.CutPrefix(r.URL.Path, "/share/")
		if !ok || token == "" {
			return false
		}
		// Never indexed, never cached: the link is the whole authorisation and
		// a revoked one must not come back from a cache.
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Cache-Control", "no-store")
		title, desc, status := "", "", http.StatusOK
		if shareTitle != nil {
			t, found, err := shareTitle(r.Context(), token)
			switch {
			case found:
				title, desc = t, shareDesc
			case err == nil:
				// Unknown or revoked: a real 404, with the site card, the same
				// answer a crawler and a monitor get for a page nowhere.
				status = http.StatusNotFound
			}
			// An error keeps the shell with the site card: an outage must not
			// turn every open link into a dead one.
		}
		body := placeholderHTML
		if shellErr == nil {
			body = renderShell(shell, r, title, desc)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Vary", "X-Forwarded-Host, X-Forwarded-Proto")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return true
	}
	return spa.Handler(dist, spa.Options{
		Routes:      isRoute,
		Placeholder: placeholderHTML,
		Gate:        func(w http.ResponseWriter, r *http.Request) bool { return !sharePage(w, r) },
		Index:       func(index []byte, r *http.Request) []byte { return renderShell(index, r, "", "") },
	})
}
