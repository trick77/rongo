package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestConfigOverridesReplaceTheLaneNamesOnTheWire: the harness points a lane
// at another model; the product, which sets nothing, keeps the constants.
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

	c := NewClient(Config{BaseURL: srv.URL, Pro: "other-pro", ShortGate: "other-gate"}, srv.Client())
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, ShortGate()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "other-pro" || seen[1] != "other-gate" {
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
