package httpapi

import (
	"net/http"
	"net/http/httptest"
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
