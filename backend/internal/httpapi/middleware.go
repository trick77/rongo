package httpapi

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/trick77/ship/kit/httpx"
)

// logging emits one access line per request. It logs the path and never the
// query string or full URL: query strings carry tokens and OIDC codes. The
// path is masked too, because a share link's token is a path segment and is
// the link's whole authorisation; see maskPath.
//
// /healthz and /readyz are not logged, whatever the status. The container
// probes them every few seconds, and a log that is mostly probes hides the
// requests a reader is looking for. A failing readiness check is kit's to
// log: once when it starts failing, once when it recovers.
// probePaths are where httpx.Health mounts the probes, read once.
var probePaths = httpx.DefaultProbePaths()

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// kit's status writer: Flush and Hijack reach the wrapped writer, so an
		// SSE handler's `w.(http.Flusher)` holds through the chain.
		rec := httpx.NewStatusWriter(w)

		next.ServeHTTP(rec, r)

		// 0: the handler wrote nothing, net/http sends 200 when it returns.
		status := rec.Status()
		if status == 0 {
			status = http.StatusOK
		}
		if slices.Contains(probePaths, r.URL.Path) {
			return
		}
		attrs := []any{
			"method", r.Method,
			"path", maskPath(r.URL.Path),
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		switch {
		case status >= 500:
			slog.Error("request", attrs...)
		case status >= 400:
			slog.Warn("request", attrs...)
		default:
			slog.Info("request", attrs...)
		}
	})
}

// recovery turns a panic into a 500 without killing the process.
func recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic recovered", "err", v, "path", maskPath(r.URL.Path))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// sharePrefixes are the routes whose next path segment is a share token:
// the public API and the page a link opens.
var sharePrefixes = []string{"/api/shares/", "/share/"}

// maskPath replaces a share token in path with the literal "{token}", keeping
// what follows it, so a log line still says which route was hit without
// handing whoever reads the log every link that was opened.
func maskPath(path string) string {
	for _, prefix := range sharePrefixes {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok || rest == "" {
			continue
		}
		suffix := ""
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			suffix = rest[i:]
		}
		return prefix + "{token}" + suffix
	}
	return path
}
