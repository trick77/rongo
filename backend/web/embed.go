// Package web serves the embedded single-page application.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// placeholderHTML ships separately from dist/, which `vite build` empties and
// repopulates on every `make build` (emptyOutDir: true). Embedding it inside
// dist would mean the build permanently overwrites the tracked placeholder
// with the built shell the first time anyone runs `make build`.
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

// Handler serves the built SPA. Any path that is not a real file falls back to
// index.html so the client-side router can take over; /api paths are excluded
// so a typo in an endpoint stays a 404 instead of silently returning HTML. If
// the SPA has never been built, dist/index.html is absent (only .gitkeep is
// tracked there) and the handler serves the placeholder instead.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist directory missing from embed: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))
	_, builtIndexErr := fs.Stat(sub, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "/api" (no trailing slash) must also be excluded, not just "/api/" —
		// a prefix-only check on "/api/" lets the bare form fall through to
		// the SPA shell instead of a 404.
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if builtIndexErr != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(placeholderHTML)
			return
		}
		if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
