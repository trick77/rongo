package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/trick77/llmwire"
	"github.com/trick77/llmwire/llmwiretest"

	"github.com/trick77/rongo/internal/llm/llmtest"
)

// intentClient is a client on llmwiretest's fake endpoint with both lanes on
// the synthetic chat model, so what reaches the wire is read in llmwiretest's
// vocabulary rather than any vendor's.
func intentClient(t *testing.T, temp *float64) (*Client, *llmwiretest.Server) {
	t.Helper()
	srv := llmwiretest.NewServer(t)
	c, err := NewClient(Config{BaseURL: srv.URL, Registry: llmtest.Registry(),
		Answer: llmtest.Answer, Gate: llmtest.Answer, GateTemperature: temp}, srv.Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func say(t *testing.T, c *Client, opts ...Option) {
	t.Helper()
	if _, _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, opts...); err != nil {
		t.Fatal(err)
	}
}

// A gate call asks for the shallowest reasoning the model allows; an answer
// call leaves the model at its own default. Neither names a level: that is the
// profile's.
func TestReasoning_gateIsMinimalAndTheAnswerIsTheModelsDefault(t *testing.T) {
	c, srv := intentClient(t, nil)

	say(t, c, ShortGate(), WithoutThinking())
	if got := srv.Last().Reasoning(); got != llmwiretest.MinimalSent {
		t.Errorf("gate reasoning = %q, want minimal (%q)", got, llmwiretest.MinimalSent)
	}

	say(t, c)
	if got := srv.Last().Reasoning(); got != "" {
		t.Errorf("answer reasoning = %q, want none sent: the model's default", got)
	}
}

// A call's cap is the answer it may write; the reasoning allowance on top is
// llmwire's, for the setting actually sent.
func TestMaxAnswerTokens_capsTheAnswerAndLeavesReasoningToTheProfile(t *testing.T) {
	c, srv := intentClient(t, nil)

	say(t, c, WithoutThinking(), WithMaxAnswerTokens(64))
	if got, _ := srv.Last().MaxTokens(); got != 64+llmwiretest.MinimalOverhead {
		t.Errorf("gate cap = %d, want 64 plus the minimal overhead", got)
	}

	say(t, c, WithMaxAnswerTokens(64))
	if got, _ := srv.Last().MaxTokens(); got <= 64 {
		t.Errorf("answer cap = %d, want 64 plus room for the default reasoning", got)
	}

	say(t, c)
	if got, ok := srv.Last().MaxTokens(); !ok || got < defaultMaxAnswerTokens {
		t.Errorf("uncapped call sent %d, want at least the default answer cap %d", got, defaultMaxAnswerTokens)
	}
}

// The pin is rongo's policy value, sent only by a call that asks for it; nil
// sends none.
func TestGateTemperature_isThePoliciesValueOnlyWhereAsked(t *testing.T) {
	half := 0.5
	c, srv := intentClient(t, &half)
	say(t, c, WithGateTemperature())
	if got := srv.Last().Body["temperature"]; got != 0.5 {
		t.Errorf("pinned temperature = %v, want 0.5", got)
	}
	say(t, c)
	if _, has := srv.Last().Body["temperature"]; has {
		t.Error("a call that does not pin must send no temperature")
	}

	c, srv = intentClient(t, nil)
	say(t, c, WithGateTemperature())
	if _, has := srv.Last().Body["temperature"]; has {
		t.Error("a nil policy temperature must send none")
	}
}

// Each lane goes to its own model's host with its own provider's key: gate and
// answer may live with different vendors.
func TestLanes_mayLiveOnDifferentProviders(t *testing.T) {
	answer, gate := llmwiretest.NewServer(t), llmwiretest.NewServer(t)
	lookup := func(name string) (string, bool) {
		switch name {
		case "LLMWIRE_RONGOTEST_BASE_URL":
			return gate.URL, true
		case "LLMWIRE_RONGOTEST_API_KEY":
			return "gate-key", true
		}
		return answer.Lookup(name)
	}
	c, err := NewClient(Config{Registry: llmtest.Registry(), Lookup: lookup,
		Answer: llmtest.Answer, Gate: llmtest.Gate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	say(t, c)
	say(t, c, ShortGate())
	if n := len(answer.Requests()); n != 1 || answer.Last().Model() != llmtest.Answer {
		t.Errorf("answer host saw %d requests (last %q), want one for %s", n, answer.Last().Model(), llmtest.Answer)
	}
	if n := len(gate.Requests()); n != 1 || gate.Last().Model() != llmtest.Gate {
		t.Errorf("gate host saw %d requests (last %q), want one for %s", n, gate.Last().Model(), llmtest.Gate)
	}
	if got := gate.Last().Header.Get("Authorization"); got != "Bearer gate-key" {
		t.Errorf("gate auth = %q, want the gate provider's key", got)
	}
}

// Each lane's model is checked at boot for what the lane does with it. The
// error names the variable and lists what would do.
func TestNewClient_refusesAModelThatCannotFillItsLane(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	for name, tc := range map[string]struct {
		answer, gate, variable string
	}{
		"gate without tools":      {llmtest.Answer, llmtest.NoTools, "BACKEND_LLM_GATE_MODEL"},
		"embeddings model answer": {llmtest.Embed, llmtest.Gate, "BACKEND_LLM_MODEL"},
		"unknown gate":            {llmtest.Answer, "nobody-knows", "BACKEND_LLM_GATE_MODEL"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewClient(Config{BaseURL: srv.URL, Registry: llmtest.Registry(),
				Answer: tc.answer, Gate: tc.gate}, srv.Server.Client())
			if err == nil {
				t.Fatal("want a boot error")
			}
			for _, want := range []string{tc.variable, "valid choices are", llmtest.Answer} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to mention %q", err, want)
				}
			}
		})
	}
	if len(srv.Requests()) != 0 {
		t.Error("a refused lane must never reach the wire")
	}
}

func TestNewClient_anUnknownModelIsLlmwiresError(t *testing.T) {
	_, err := NewClient(Config{BaseURL: "http://127.0.0.1:1", Registry: llmtest.Registry(),
		Answer: "nobody-knows", Gate: llmtest.Gate}, nil)
	var unknown *llmwire.UnknownModelError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want llmwire's UnknownModelError wrapped", err)
	}
}
