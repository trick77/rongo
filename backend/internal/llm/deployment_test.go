package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

	c := mustClient(t, Config{BaseURL: srv.URL, Pro: "glm-5.3-flash", ShortGate: "glm-5.3-flash"}, srv.Client())
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
	plain := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())
	if _, _, err := plain.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, ShortGate()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != ShortGateDeployment {
		t.Errorf("without overrides the constant must go on the wire, got %v", seen)
	}
}

// TestConfigOverrideToAnUnknownModelFailsTheBoot: the client is built for the
// Pro lane's profile, so a deployment naming a model llmwire has no profile
// for is told so at boot, with the ids it could have used, rather than with
// a quality number measured against a model nobody described. The gate lane
// is checked the same way.
func TestConfigOverrideToAnUnknownModelFailsTheBoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unknown model must never reach the wire")
	}))
	t.Cleanup(srv.Close)

	for _, cfg := range []Config{
		{BaseURL: srv.URL, Pro: "other-pro"},
		{BaseURL: srv.URL, ShortGate: "other-gate"},
	} {
		_, err := NewClient(cfg, srv.Client())
		var unknown *llmwire.UnknownModelError
		if !errors.As(err, &unknown) {
			t.Fatalf("%+v: err = %v, want llmwire's UnknownModelError", cfg, err)
		}
	}
}

// TestConfigOverridesMustShareAHost: one client, one host. A gate lane that
// llmwire would reach through another provider is refused at boot rather
// than failing the first question with an unknown-model 400 from the Pro
// host. Through the gateway both lanes carry the gateway's provider, so
// listing both passes and listing one fails.
func TestConfigOverridesMustShareAHost(t *testing.T) {
	env := map[string]string{
		"LLMWIRE_LITELLM_BASE_URL": "https://gw.example/v1",
		"LLMWIRE_LITELLM_API_KEY":  "k",
		"LLMWIRE_MIMO_API_KEY":     "k",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	t.Setenv(llmwire.GatewayModelsEnv, "gpt-5.4-mini=proxy")
	if _, err := NewClient(Config{Pro: "gpt-5.4-mini"}, nil); err == nil {
		t.Fatal("gate on MiMo's host with Pro on the gateway must be refused")
	}

	t.Setenv(llmwire.GatewayModelsEnv, "gpt-5.4-mini=proxy,mimo-v2.5=proxy-gate")
	if _, err := NewClient(Config{Pro: "gpt-5.4-mini"}, nil); err != nil {
		t.Fatalf("both lanes on the gateway: %v", err)
	}
}

// TestGateKnobsAreBestEffort: a model that refuses a pinned temperature or
// cannot stop thinking still gets the call, with the nearest request it
// accepts, because those knobs are preferences of the gate calls and not
// what the deployment chose the model for.
func TestGateKnobsAreBestEffort(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	t.Cleanup(srv.Close)

	// gpt-5.4-mini's profile forces temperature to 1 and takes reasoning off
	// as effort "none".
	var logged bytes.Buffer
	c := mustClient(t, Config{BaseURL: srv.URL, Pro: "gpt-5.4-mini", ShortGate: "gpt-5.4-mini",
		Logger: slog.New(slog.NewTextHandler(&logged, nil))}, srv.Client())
	for range 2 {
		_, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}},
			ShortGate(), WithoutThinking(), WithTemperature(0))
		if err != nil {
			t.Fatalf("the call must go out with the nearest accepted request: %v", err)
		}
	}
	if body["temperature"] != 1.0 {
		t.Errorf("temperature on the wire = %v, want the forced 1", body["temperature"])
	}
	// Said once at Warn, where an operator reading a quality complaint at the
	// default log level finds it; not once per call.
	if n := strings.Count(logged.String(), "level=WARN msg=\"llm: request knob refused"); n != 1 {
		t.Errorf("demotion warned %d times, want once:\n%s", n, logged.String())
	}
}
