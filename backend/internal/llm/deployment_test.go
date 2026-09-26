package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/llmwire/llmwiretest"

	"github.com/trick77/rongo/internal/llm/llmtest"
)

// TestConfiguredModelsGoOnTheWire: each lane sends the model it was
// configured with.
func TestConfiguredModelsGoOnTheWire(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Server.Client())
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, ShortGate()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || reqs[0].Model() != testAnswerModel || reqs[1].Model() != testGateModel {
		t.Errorf("models on the wire = %v, want the configured ones", reqs)
	}
}

// TestGateKnobsAreBestEffort: a model that refuses a pinned temperature still
// gets the call, with the nearest request it accepts, because the pin is a
// preference of the gate calls and not what the deployment chose the model
// for. llmwiretest's budget model takes no temperature.
func TestGateKnobsAreBestEffort(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	var logged bytes.Buffer
	zero := 0.0
	c, err := NewClient(Config{BaseURL: srv.URL, Registry: llmtest.Registry(),
		Answer: llmtest.NoTools, Gate: llmtest.Gate, GateTemperature: &zero,
		Logger: slog.New(slog.NewTextHandler(&logged, nil))}, srv.Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}},
			WithGateTemperature()); err != nil {
			t.Fatalf("the call must go out with the nearest accepted request: %v", err)
		}
	}
	if _, has := srv.Last().Body["temperature"]; has {
		t.Errorf("temperature on the wire = %v, want it dropped", srv.Last().Body["temperature"])
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
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{BaseURL: "http://127.0.0.1:1", Gate: testGateModel}, "BACKEND_LLM_MODEL"},
		{Config{BaseURL: "http://127.0.0.1:1", Answer: testAnswerModel}, "BACKEND_LLM_GATE_MODEL"},
	} {
		_, err := NewClient(tc.cfg, nil)
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
