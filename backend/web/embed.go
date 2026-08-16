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

// Handler serves the built SPA. Any path that is not a real file falls back to
// index.html so the client-side router can take over; /api paths are excluded
// so a typo in an endpoint stays a 404 instead of silently returning HTML.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist directory missing from embed: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "/api" (no trailing slash) must also be excluded, not just "/api/" —
		// a prefix-only check on "/api/" lets the bare form fall through to
		// the SPA shell instead of a 404.
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
