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
	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
