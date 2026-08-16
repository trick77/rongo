package httpapi

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// statusRecorder captures the status code so the access log can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the wrapped ResponseWriter so http.ResponseController can
// reach optional interfaces implemented by it. This alone does NOT make
// statusRecorder itself satisfy those interfaces — a handler doing the
// conventional `w.(http.Flusher)` type assertion sees statusRecorder, which
// has no Flush method, and gets ok == false regardless of Unwrap. Flush and
// Hijack below make the type assertion succeed too, so both the
// http.ResponseController path and the direct interface-assertion path work
// through this middleware.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush forwards to the wrapped ResponseWriter's Flush when it supports it.
// Without this, a phase-2/phase-4 SSE handler doing `w.(http.Flusher)` gets
// ok == false and, if that's ignored, silently buffers the whole streamed
// response instead of flushing it incrementally.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the wrapped ResponseWriter's Hijack when it supports it,
// for completeness alongside Flush/Unwrap.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// logging emits one access line per request. It logs r.URL.Path and never the
// query string or full URL: query strings carry tokens and, later, OIDC codes.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		switch {
		case rec.status >= 500:
			slog.Error("request", attrs...)
		case rec.status >= 400:
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
				slog.Error("panic recovered", "err", v, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
