package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLogging_flushSucceedsThroughTheChain guards against statusRecorder
// hiding http.Flusher from a wrapped handler. Without Unwrap(), a phase-2 SSE
// handler calling http.NewResponseController(w).Flush() from inside the
// logging middleware would get http.ErrNotSupported and, if that error is
// ignored, buffer the whole streamed response instead of flushing it.
func TestLogging_flushSucceedsThroughTheChain(t *testing.T) {
	// Given
	var flushErr error
	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk"))
		flushErr = http.NewResponseController(w).Flush()
	}))

	// When
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Then
	if flushErr != nil {
		t.Fatalf("Flush() err = %v, want nil", flushErr)
	}
}

// TestLogging_flusherTypeAssertionSucceedsThroughTheChain guards the other
// path to Flush: the conventional `w.(http.Flusher)` type assertion a
// handler does directly, without going through http.ResponseController.
// Unwrap() alone does not satisfy this — statusRecorder needs its own
// Flush method so the assertion sees ok == true.
func TestLogging_flusherTypeAssertionSucceedsThroughTheChain(t *testing.T) {
	// Given
	var asserted bool
	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk"))
		f, ok := w.(http.Flusher)
		asserted = ok
		if ok {
			f.Flush()
		}
	}))

	// When
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Then
	if !asserted {
		t.Fatal("w.(http.Flusher) ok = false, want true")
	}
}

// TestLogging_aHealthyProbeIsNotLogged: the container probes /healthz every
// few seconds, and a log that is mostly probes hides everything else. A
// failing probe still logs.
func TestLogging_aHealthyProbeIsNotLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	status := http.StatusOK
	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if buf.Len() != 0 {
		t.Errorf("a healthy probe was logged: %s", buf.String())
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/threads", nil))
	if !strings.Contains(buf.String(), "path=/api/threads") {
		t.Errorf("an ordinary request was not logged: %s", buf.String())
	}

	buf.Reset()
	status = http.StatusServiceUnavailable
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(buf.String(), "path=/healthz") {
		t.Errorf("a failing probe was not logged: %s", buf.String())
	}
}

// A share link's token is its whole authorisation, and it travels in the
// path, so the access log masks it the way it leaves a query string out.
func TestLoggingMasksShareToken(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	const token = "kd8Qw1rZx3Yv9pLmN0aB_c"
	handler := logging(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

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

func TestRecoveryMasksShareToken(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	const token = "kd8Qw1rZx3Yv9pLmN0aB_c"
	handler := recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/share/"+token, nil))

	if strings.Contains(buf.String(), token) {
		t.Errorf("token in the panic log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "path=/share/{token}") {
		t.Errorf("panic log does not say which route: %s", buf.String())
	}
}
