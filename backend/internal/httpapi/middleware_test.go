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
