package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLog routes slog's default, which chain reads, into a buffer for the
// test's lifetime, at debug: kit logs a routine request (2xx, 3xx) there.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestChain_flushSucceedsThroughTheChain guards against the status writer
// hiding http.Flusher from a wrapped handler. Without Unwrap(), an SSE
// handler calling http.NewResponseController(w).Flush() from inside the
// access log would get http.ErrNotSupported and, if that error is ignored,
// buffer the whole streamed response instead of flushing it.
func TestChain_flushSucceedsThroughTheChain(t *testing.T) {
	var flushErr error
	handler := chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk"))
		flushErr = http.NewResponseController(w).Flush()
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))

	if flushErr != nil {
		t.Fatalf("Flush() err = %v, want nil", flushErr)
	}
}

// TestChain_flusherTypeAssertionSucceedsThroughTheChain guards the other
// path to Flush: the conventional `w.(http.Flusher)` type assertion a
// handler does directly, without going through http.ResponseController.
// Unwrap() alone does not satisfy this; kit's StatusWriter has its own Flush
// so the assertion sees ok == true.
func TestChain_flusherTypeAssertionSucceedsThroughTheChain(t *testing.T) {
	var asserted bool
	handler := chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk"))
		f, ok := w.(http.Flusher)
		asserted = ok
		if ok {
			f.Flush()
		}
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))

	if !asserted {
		t.Fatal("w.(http.Flusher) ok = false, want true")
	}
}

// TestChain_aProbeIsNotLogged: the container probes /healthz and /readyz
// every few seconds, and a log that is mostly probes hides everything else.
// A failing one neither: kit logs readiness transitions, not probes.
func TestChain_aProbeIsNotLogged(t *testing.T) {
	buf := captureLog(t)
	status := http.StatusOK
	handler := chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))

	for _, p := range []string{"/healthz", "/readyz"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	if buf.Len() != 0 {
		t.Errorf("a healthy probe was logged: %s", buf.String())
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/threads", nil))
	if !strings.Contains(buf.String(), "path=/api/threads") {
		t.Errorf("an ordinary request was not logged: %s", buf.String())
	}

	buf.Reset()
	status = http.StatusServiceUnavailable
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if buf.Len() != 0 {
		t.Errorf("a failing probe was logged per request: %s", buf.String())
	}
}

// A share link's token is its whole authorisation, and it travels in the
// path, so the access log masks it the way it leaves a query string out.
func TestChain_masksShareTokenInTheAccessLog(t *testing.T) {
	buf := captureLog(t)
	const token = "kd8Qw1rZx3Yv9pLmN0aB_c"
	handler := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, path := range []string{
		"/api/shares/" + token,
		"/api/shares/" + token + "/source",
		"/share/" + token,
	} {
		buf.Reset()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(buf.String(), token) {
			t.Errorf("%s: token in the access log: %s", path, buf.String())
		}
		if !strings.Contains(buf.String(), "{token}") {
			t.Errorf("%s: no masked segment in the access log: %s", path, buf.String())
		}
	}
	buf.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/shares/"+token+"/source", nil))
	if !strings.Contains(buf.String(), "path=/api/shares/{token}/source") {
		t.Errorf("the route after the token was lost: %s", buf.String())
	}
	buf.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/shares", nil))
	if !strings.Contains(buf.String(), "path=/api/shares ") {
		t.Errorf("the owner's list route was changed: %s", buf.String())
	}
}

// The panic line masks the token the same way, and the 500 carries the
// family's error body.
func TestChain_masksShareTokenInThePanicLine(t *testing.T) {
	buf := captureLog(t)
	const token = "kd8Qw1rZx3Yv9pLmN0aB_c"
	handler := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/"+token, nil))

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("response = %d %s, want a 500 with an error body", rec.Code, rec.Body.String())
	}
	if strings.Contains(buf.String(), token) {
		t.Errorf("token in the panic log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "path=/share/{token}") {
		t.Errorf("panic log does not say which route: %s", buf.String())
	}
}

// The security headers and the cross-site refusal are kit's, on every
// response: a cross-site POST never reaches the router.
func TestChain_refusesACrossSitePostAndSetsTheHeaders(t *testing.T) {
	handler := chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "'wasm-unsafe-eval'") {
		t.Errorf("Content-Security-Policy = %q, want the WebAssembly allowance the diagrams need", got)
	}
	if rec.Header().Get("X-Content-Type-Options") == "" {
		t.Error("no X-Content-Type-Options: the security headers are missing")
	}
}
