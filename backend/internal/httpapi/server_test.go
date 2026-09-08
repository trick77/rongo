package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/rongo/internal/version"
)

func TestHealthz_returnsOK(t *testing.T) {
	// Given
	srv := NewServer(Deps{})

	// When
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestUnknownRoute_returns404(t *testing.T) {
	srv := NewServer(Deps{})

	req := httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// The composer's footer names the build that answered, and this is the request
// it reads it from: the session check the UI already makes before it renders.
func TestMe_carriesTheBuildVersion(t *testing.T) {
	// Given: a server, and a stamped binary
	srv := newTestServer(t)
	prev := version.Version
	t.Cleanup(func() { version.Version = prev })
	version.Version = "9.9.9"

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Email   string `json:"email"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != "9.9.9" {
		t.Errorf("version = %q, want %q", body.Version, "9.9.9")
	}
	if body.Email == "" {
		t.Error("email is empty; the session fields must survive the addition")
	}
}
