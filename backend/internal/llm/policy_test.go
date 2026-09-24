package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wireRecorder answers every call and keeps the last request body.
func wireRecorder(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	body := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clear(body)
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

// TestPolicy_defaultIsWhatRongoWasMeasuredWith: no policy set renders the
// MiMo-era wire, so an existing deployment sees no change.
func TestPolicy_defaultIsWhatRongoWasMeasuredWith(t *testing.T) {
	srv, body := wireRecorder(t)
	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())

	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}},
		ShortGate(), WithoutThinking(), WithGateTemperature()); err != nil {
		t.Fatal(err)
	}
	if (*body)["temperature"] != 0.0 {
		t.Errorf("gate temperature = %v, want the pinned 0", (*body)["temperature"])
	}
	if th, _ := (*body)["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Errorf("gate thinking = %v, want MiMo's disabled toggle", (*body)["thinking"])
	}

	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, has := (*body)["temperature"]; has {
		t.Errorf("the answer lane must send no temperature, got %v", (*body)["temperature"])
	}
	if _, has := (*body)["thinking"]; has {
		t.Errorf("the answer lane must leave reasoning at the model's default, got %v", (*body)["thinking"])
	}
}

// TestPolicy_rendersTheDeploymentsAnswers: the call sites keep their intent
// (pin, gate); the policy decides what reaches the wire.
func TestPolicy_rendersTheDeploymentsAnswers(t *testing.T) {
	srv, body := wireRecorder(t)
	c := mustClient(t, Config{BaseURL: srv.URL, Answer: "gpt-5.4-mini", Gate: "gpt-5.4-mini",
		Policy: Policy{GateTemperature: nil, GateReasoning: "low", ProReasoning: "medium"}}, srv.Client())

	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}},
		ShortGate(), WithoutThinking(), WithGateTemperature()); err != nil {
		t.Fatal(err)
	}
	if _, has := (*body)["temperature"]; has {
		t.Errorf("GateTemperature nil must send none, got %v", (*body)["temperature"])
	}
	if (*body)["reasoning_effort"] != "low" {
		t.Errorf("gate reasoning = %v, want low", (*body)["reasoning_effort"])
	}

	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if (*body)["reasoning_effort"] != "medium" {
		t.Errorf("answer reasoning = %v, want medium", (*body)["reasoning_effort"])
	}
}

// TestPolicy_isCheckedAgainstTheModelAtBoot: a level the model does not
// have, or off on a model that cannot stop, is a configuration error with a
// known answer and is refused before the first question.
func TestPolicy_isCheckedAgainstTheModelAtBoot(t *testing.T) {
	srv, _ := wireRecorder(t)
	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"level the gate model lacks": {
			Config{BaseURL: srv.URL, Answer: testAnswerModel, Gate: testGateModel, Policy: Policy{GateReasoning: "xhigh"}},
			"BACKEND_LLM_GATE_REASONING",
		},
		"off on a model that cannot stop": {
			Config{BaseURL: srv.URL, Answer: "glm-5.3-flash", Gate: "glm-5.3-flash", Policy: Policy{ProReasoning: ReasoningOff}},
			"cannot stop thinking",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewClient(tc.cfg, srv.Client())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
	// default always passes, whatever the model.
	if _, err := NewClient(Config{BaseURL: srv.URL, Answer: "glm-5.3-flash", Gate: "glm-5.3-flash",
		Policy: Policy{GateReasoning: ReasoningDefault, ProReasoning: ReasoningDefault}}, srv.Client()); err != nil {
		t.Fatal(err)
	}
}
