package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/llmwire"
)

// TestConfigOverridesReplaceTheLaneNamesOnTheWire: the harness points a lane
// at another model; the product, which sets nothing, keeps the constants. The
// other model has to be one llmwire's registry knows, so the one Z.ai profile
// it ships stands in for "some other deployment".
func TestConfigOverridesReplaceTheLaneNamesOnTheWire(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, req.Model)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(Config{BaseURL: srv.URL, Pro: "glm-5.3-flash", ShortGate: "glm-5.3-flash"}, srv.Client())
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, ShortGate()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "glm-5.3-flash" || seen[1] != "glm-5.3-flash" {
		t.Errorf("models on the wire = %v, want the overrides", seen)
	}

	seen = nil
	plain := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	if _, _, err := plain.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, ShortGate()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != ShortGateDeployment {
		t.Errorf("without overrides the constant must go on the wire, got %v", seen)
	}
}

// TestConfigOverrideToAnUnknownModelFailsTheCall: llmwire validates every
// request against its registry, so a harness naming a model it has no profile
// for gets told so on the first call, with the ids it could have used, rather
// than a quality number measured against a model nobody described.
func TestConfigOverrideToAnUnknownModelFailsTheCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unknown model must never reach the wire")
	}))
	t.Cleanup(srv.Close)

	c := NewClient(Config{BaseURL: srv.URL, Pro: "other-pro"}, srv.Client())
	_, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})

	var unknown *llmwire.UnknownModelError
	if !errors.As(err, &unknown) || unknown.ID != "other-pro" {
		t.Fatalf("err = %v, want llmwire's UnknownModelError for other-pro", err)
	}
}
