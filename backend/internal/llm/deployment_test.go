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

// TestConfiguredModelsGoOnTheWire: each lane sends the model it was
// configured with. It has to be one llmwire's registry knows, so the one Z.ai
// profile it ships stands in for "some other deployment".
func TestConfiguredModelsGoOnTheWire(t *testing.T) {
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

	// glm cannot stop thinking, so the default gate policy (off) is refused
	// at boot; a deployment on it has to say what a gate call does instead.
	c := mustClient(t, Config{BaseURL: srv.URL, Answer: "glm-5.3-flash", Gate: "glm-5.3-flash",
		Policy: Policy{GateReasoning: "low", ProReasoning: ReasoningDefault}}, srv.Client())
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, ShortGate()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "glm-5.3-flash" || seen[1] != "glm-5.3-flash" {
		t.Errorf("models on the wire = %v, want the configured ones", seen)
	}
}

// TestConfigOverrideToAnUnknownModelFailsTheBoot: the client is built for the
// answer lane's profile, so a deployment naming a model llmwire has no profile
// for is told so at boot, with the ids it could have used, rather than with
// a quality number measured against a model nobody described. The gate lane
// is checked the same way.
func TestConfigOverrideToAnUnknownModelFailsTheBoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("an unknown model must never reach the wire")
	}))
	t.Cleanup(srv.Close)

	for _, cfg := range []Config{
		{BaseURL: srv.URL, Answer: "other-answer", Gate: testGateModel},
		{BaseURL: srv.URL, Answer: testAnswerModel, Gate: "other-gate"},
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
// than failing the first question with an unknown-model 400 from the
// answer host. Through the gateway both lanes carry the gateway's provider, so
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
	if _, err := NewClient(Config{Answer: "gpt-5.4-mini", Gate: "mimo-v2.6-flash"}, nil); err == nil {
		t.Fatal("gate on MiMo's host with the answer lane on the gateway must be refused")
	}

	t.Setenv(llmwire.GatewayModelsEnv, "gpt-5.4-mini=proxy,mimo-v2.6-flash=proxy-gate")
	if _, err := NewClient(Config{Answer: "gpt-5.4-mini", Gate: "mimo-v2.6-flash"}, nil); err != nil {
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
	c := mustClient(t, Config{BaseURL: srv.URL, Answer: "gpt-5.4-mini", Gate: "gpt-5.4-mini",
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

// TestNewClient_refusesAnUnconfiguredLane: rongo has no model of its own.
// A lane nobody configured is a boot error naming the variable, never a
// fallback to a model somebody once measured.
func TestNewClient_refusesAnUnconfiguredLane(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("an unconfigured client must never reach the wire")
	}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{BaseURL: srv.URL, Gate: testGateModel}, "BACKEND_LLM_MODEL"},
		{Config{BaseURL: srv.URL, Answer: testAnswerModel}, "BACKEND_LLM_GATE_MODEL"},
	} {
		_, err := NewClient(tc.cfg, srv.Client())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want one naming %s", tc.cfg, err, tc.want)
		}
	}
}

// TestLanesStayApartOnOneModel: the lanes are told apart by name, not by the
// model behind them, so both on one model still lets each be moved alone.
func TestLanesStayApartOnOneModel(t *testing.T) {
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

	same := mustClient(t, Config{BaseURL: srv.URL, Answer: testGateModel, Gate: testGateModel}, srv.Client())
	if same.Deployment(LaneAnswer) != testGateModel || same.Deployment(LaneGate) != testGateModel {
		t.Fatalf("lanes = %s/%s, want both %s", same.Deployment(LaneAnswer), same.Deployment(LaneGate), testGateModel)
	}

	moved := mustClient(t, Config{BaseURL: srv.URL, Answer: testGateModel, Gate: testAnswerModel}, srv.Client())
	msgs := []Message{{Role: "user", Content: "x"}}
	for _, opts := range [][]Option{nil, {Pro()}, {ShortGate()}} {
		if _, _, err := moved.Complete(context.Background(), msgs, opts...); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{testGateModel, testGateModel, testAnswerModel}; strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("models on the wire = %v, want %v", seen, want)
	}
}
