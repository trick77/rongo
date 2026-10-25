package llm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trick77/llmwire"
	"github.com/trick77/llmwire/llmwiretest"

	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/llm/llmtest"
)

// defaultBound is what a call streams inside config.DefaultLLMTimeout at
// config.SlowestStreamTokensPerSecond: the Balanced bound with no Timeout set.
var defaultBound = int(config.DefaultLLMTimeout/time.Second) * config.SlowestStreamTokensPerSecond

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

// laneClient is a client on llmwiretest's fake endpoint with the answer lane
// on answer, the gate lane on Gate, pinned at level ("" = unpinned).
func laneClient(t *testing.T, answer, level string, timeout time.Duration) (*Client, *llmwiretest.Server) {
	t.Helper()
	srv := llmwiretest.NewServer(t)
	c, err := NewClient(Config{BaseURL: srv.URL, Registry: llmtest.Registry(),
		Answer: answer, Gate: llmtest.Gate, AnswerReasoning: level, Timeout: timeout}, srv.Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

// An unpinned answer call sends no knob while answer plus the model's no-knob
// allowance streams inside the call timeout (unset: config.DefaultLLMTimeout),
// and Balanced past it. Decided per call, on that call's budget.
func TestReasoning_anUnpinnedAnswerIsBalancedOnlyWhereNoKnobOutrunsTheTimeout(t *testing.T) {
	over := defaultBound - llmtest.DeepNoKnobOverhead + 1
	c, srv := laneClient(t, llmtest.Deep, "", 0)

	say(t, c, WithMaxAnswerTokens(over))
	if got := srv.Last().Reasoning(); got != llmwiretest.BalancedSent {
		t.Errorf("past the bound: reasoning = %q, want balanced (%q)", got, llmwiretest.BalancedSent)
	}
	if got, _ := srv.Last().MaxTokens(); got != over+llmwiretest.BalancedOverhead {
		t.Errorf("past the bound: cap = %d, want %d plus the balanced overhead", got, over)
	}
	say(t, c, Pro(), WithMaxAnswerTokens(over))
	if got := srv.Last().Reasoning(); got != llmwiretest.BalancedSent {
		t.Errorf("Pro() past the bound: reasoning = %q, want balanced", got)
	}

	// One token less fits: no knob, the cap is the no-knob allowance's.
	say(t, c, WithMaxAnswerTokens(over-1))
	if got := srv.Last().Reasoning(); got != "" {
		t.Errorf("at the bound: reasoning = %q, want none sent", got)
	}
	if got, _ := srv.Last().MaxTokens(); got != defaultBound {
		t.Errorf("at the bound: cap = %d, want %d", got, defaultBound)
	}

	// A smaller budget on the same model, and the gate lane, stay as they were.
	say(t, c)
	if got, _ := srv.Last().MaxTokens(); srv.Last().Reasoning() != "" || got != defaultMaxAnswerTokens+llmtest.DeepNoKnobOverhead {
		t.Errorf("default budget: reasoning = %q cap = %d, want none and %d", srv.Last().Reasoning(), got,
			defaultMaxAnswerTokens+llmtest.DeepNoKnobOverhead)
	}
	gsrv := llmwiretest.NewServer(t)
	g, err := NewClient(Config{BaseURL: gsrv.URL, Registry: llmtest.Registry(),
		Answer: llmtest.Answer, Gate: llmtest.Deep}, gsrv.Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	say(t, g, ShortGate(), WithMaxAnswerTokens(over))
	if got := gsrv.Last().Reasoning(); got != "" {
		t.Errorf("gate lane past the bound sent %q, want none: Balanced is the answer lane's", got)
	}
}

// Under the bound the request is the one an answer call sent before the rule:
// no reasoning key of any spelling, the no-knob cap.
func TestReasoning_anUnpinnedAnswerUnderTheBoundSendsNoReasoningKey(t *testing.T) {
	for _, model := range []string{llmtest.Answer, llmtest.Deep} {
		c, srv := laneClient(t, model, "", 0)
		say(t, c)
		for _, k := range []string{"reasoning_effort", "reasoning", "thinking"} {
			if v, has := srv.Last().Body[k]; has {
				t.Errorf("%s: body carries %s = %v, want no reasoning key", model, k, v)
			}
		}
	}
}

// The bound is the configured call timeout's, the one llmwire enforces: a
// shorter one sends Balanced where the default would not, a longer one keeps
// no knob where the default would not. Zero is the default.
func TestReasoning_theBoundFollowsTheConfiguredTimeout(t *testing.T) {
	// 10 minutes streams 12000 tokens: a no-knob cap of 14000 fits the
	// default's 18000, not that.
	mid := 14000 - llmtest.DeepNoKnobOverhead
	short, srv := laneClient(t, llmtest.Deep, "", 10*time.Minute)
	say(t, short, WithMaxAnswerTokens(mid))
	if got := srv.Last().Reasoning(); got != llmwiretest.BalancedSent {
		t.Errorf("10m timeout, no-knob cap 14000: reasoning = %q, want balanced (%q)", got, llmwiretest.BalancedSent)
	}
	def, dsrv := laneClient(t, llmtest.Deep, "", 0)
	say(t, def, WithMaxAnswerTokens(mid))
	if got := dsrv.Last().Reasoning(); got != "" {
		t.Errorf("default timeout, no-knob cap 14000: reasoning = %q, want none sent", got)
	}

	// 20 minutes streams 24000: past the default's bound, inside that.
	over := defaultBound - llmtest.DeepNoKnobOverhead + 1
	long, lsrv := laneClient(t, llmtest.Deep, "", 20*time.Minute)
	say(t, long, WithMaxAnswerTokens(over))
	if got := lsrv.Last().Reasoning(); got != "" {
		t.Errorf("20m timeout, no-knob cap %d: reasoning = %q, want none sent", defaultBound+1, got)
	}
	if got, _ := lsrv.Last().MaxTokens(); got != defaultBound+1 {
		t.Errorf("20m timeout: cap = %d, want the no-knob %d", got, defaultBound+1)
	}

	// Zero and DefaultLLMTimeout send the same request on either side.
	explicit, esrv := laneClient(t, llmtest.Deep, "", config.DefaultLLMTimeout)
	for _, budget := range []int{mid, over} {
		say(t, def, WithMaxAnswerTokens(budget))
		say(t, explicit, WithMaxAnswerTokens(budget))
		if !reflect.DeepEqual(dsrv.Last().Body, esrv.Last().Body) {
			t.Errorf("budget %d: Timeout %v sent %v, zero sent %v", budget, config.DefaultLLMTimeout, esrv.Last().Body, dsrv.Last().Body)
		}
	}
}

// Balanced is sent only where it shortens the cap: a model whose no-knob
// allowance is no larger than Balanced's keeps its own default past the bound.
func TestReasoning_pastTheBoundBalancedOnlyWhereItShortensTheCap(t *testing.T) {
	c, srv := laneClient(t, llmtest.Flat, "", time.Minute)
	say(t, c)
	if got := srv.Last().Reasoning(); got != "" {
		t.Errorf("reasoning = %q, want none sent: Balanced would not shorten the cap", got)
	}
	if got, _ := srv.Last().MaxTokens(); got != defaultMaxAnswerTokens+llmtest.FlatNoKnobOverhead {
		t.Errorf("cap = %d, want the no-knob %d", got, defaultMaxAnswerTokens+llmtest.FlatNoKnobOverhead)
	}
}

// An operator pin wins on both sides of the bound, whatever the timeout.
func TestReasoning_aPinnedLevelWinsOnBothSidesOfTheBound(t *testing.T) {
	over := defaultBound - llmtest.DeepNoKnobOverhead + 1
	for name, tc := range map[string]struct {
		budget  int
		timeout time.Duration
	}{
		"under":      {defaultMaxAnswerTokens, 0},
		"over":       {over, 0},
		"short over": {defaultMaxAnswerTokens, time.Minute},
		"long under": {over, 20 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			c, srv := laneClient(t, llmtest.Deep, "high", tc.timeout)
			say(t, c, WithMaxAnswerTokens(tc.budget))
			if got := srv.Last().Reasoning(); got != "high" {
				t.Errorf("reasoning = %q, want the pinned high", got)
			}
		})
	}
}

// noKnobCap and the Balanced allowance mirror llmwire's plan, which llmwire
// does not export. Checked against the cap llmwire actually puts on the wire
// for a no-knob and a Balanced request, on
// every shipped streaming model and the synthetic ones, so a llmwire bump
// that changes its rule fails here.
func TestNoKnobCap_isWhatLlmwirePlans(t *testing.T) {
	for _, tc := range []struct {
		reg  *llmwire.Registry
		gate string
	}{
		{llmwire.Default(), llmwire.Default().ChatModels(llmwire.Needs{Tools: true})[0]},
		{llmtest.Registry(), llmtest.Gate},
	} {
		for _, model := range tc.reg.ChatModels(llmwire.Needs{Streaming: true}) {
			t.Run(model, func(t *testing.T) {
				srv := llmwiretest.NewServer(t)
				c, err := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Registry: tc.reg,
					Answer: model, Gate: tc.gate}, srv.Server.Client())
				if err != nil {
					t.Fatal(err)
				}
				wire := llmwire.New(llmwire.Config{BaseURL: srv.URL, APIKey: "k", Registry: tc.reg,
					HTTPClient: srv.Server.Client()})
				sent := func(n int, r llmwire.ReasoningRequest) int {
					t.Helper()
					s, _, err := wire.ChatStream(context.Background(), llmwire.ChatRequest{Model: model,
						Messages: []llmwire.Message{llmwire.User("x")}, MaxAnswerTokens: &n, Reasoning: r})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.Collect(func(string) {}); err != nil {
						t.Fatal(err)
					}
					_ = s.Close()
					got, ok := srv.Last().MaxTokens()
					if !ok {
						t.Fatal("no cap on the wire")
					}
					return got
				}
				for _, n := range []int{1, defaultMaxAnswerTokens, defaultBound} {
					if got, want := c.noKnobCap(n), sent(n, nil); got != want {
						t.Errorf("budget %d: noKnobCap = %d, llmwire sent %d", n, got, want)
					}
					if got, want := c.planCap(n, c.answerBalanced), sent(n, llmwire.ReasoningBalanced()); got != want {
						t.Errorf("budget %d: Balanced cap = %d, llmwire sent %d", n, got, want)
					}
				}
			})
		}
	}
}

// A configured answer level is sent on answer calls only; gate calls stay
// minimal, and a call asked not to think is not overridden by it.
func TestReasoning_aConfiguredAnswerLevelReachesTheAnswerOnly(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	c, err := NewClient(Config{BaseURL: srv.URL, Registry: llmtest.Registry(),
		Answer: llmtest.Answer, Gate: llmtest.Gate, AnswerReasoning: "high"}, srv.Server.Client())
	if err != nil {
		t.Fatal(err)
	}

	say(t, c)
	if got := srv.Last().Reasoning(); got != "high" {
		t.Errorf("answer reasoning = %q, want the configured high", got)
	}
	say(t, c, ShortGate(), WithoutThinking())
	if got := srv.Last().Reasoning(); got != llmwiretest.MinimalSent {
		t.Errorf("gate reasoning = %q, want minimal (%q)", got, llmwiretest.MinimalSent)
	}
	say(t, c, ShortGate())
	if got := srv.Last().Reasoning(); got != "" {
		t.Errorf("gate call without an intent sent %q, want none: the level is the answer lane's", got)
	}
	say(t, c, WithoutThinking())
	if got := srv.Last().Reasoning(); got != llmwiretest.MinimalSent {
		t.Errorf("answer-lane call asked not to think sent %q, want minimal", got)
	}
}

// A level the answer model's profile does not list refuses the boot, naming the
// variable and what the model does take; nothing reaches the wire.
func TestNewClient_refusesAnAnswerLevelTheModelDoesNotTake(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	_, err := NewClient(Config{BaseURL: srv.URL, Registry: llmtest.Registry(),
		Answer: llmtest.Answer, Gate: llmtest.Gate, AnswerReasoning: "ultra"}, srv.Server.Client())
	if err == nil {
		t.Fatal("want a boot error")
	}
	for _, want := range []string{"BACKEND_LLM_REASONING", `"ultra"`, llmtest.Answer, "none, low, medium, high"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}

	_, err = NewClient(Config{BaseURL: srv.URL, Registry: llmtest.Registry(),
		Answer: llmtest.NoTools, Gate: llmtest.Gate, AnswerReasoning: "high"}, srv.Server.Client())
	if err == nil || !strings.Contains(err.Error(), "BACKEND_LLM_REASONING") || !strings.Contains(err.Error(), "no effort level") {
		t.Errorf("err = %v, want a refusal saying the model takes no effort level", err)
	}
	if len(srv.Requests()) != 0 {
		t.Error("a refused level must never reach the wire")
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
