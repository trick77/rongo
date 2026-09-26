package llmtest

import (
	"testing"

	"github.com/trick77/llmwire"
)

func TestRegistry_gateIsTheChatProfileOnItsOwnProvider(t *testing.T) {
	a, err := Registry().Require(Answer, llmwire.Needs{Tools: true, Streaming: true})
	if err != nil {
		t.Fatal(err)
	}
	g, err := Registry().Require(Gate, llmwire.Needs{Tools: true, Streaming: true})
	if err != nil {
		t.Fatal(err)
	}
	if g.Provider != GateProvider || a.Provider == g.Provider {
		t.Errorf("providers = %s/%s, want the gate on %s", a.Provider, g.Provider, GateProvider)
	}
	if g.Reasoning.Balanced != a.Reasoning.Balanced || g.Limits != a.Limits {
		t.Error("the gate must be the answer's profile under another id")
	}
	if _, err := Registry().Require(NoTools, llmwire.Needs{Tools: true}); err == nil {
		t.Errorf("%s must lack tools", NoTools)
	}
}

func TestWithGate_refusesADocumentWithoutTheChatProfile(t *testing.T) {
	if _, err := withGate([]byte("providers: {}\nprofiles: []\n")); err == nil {
		t.Error("want an error")
	}
	if _, err := withGate([]byte("a: 1\n")); err == nil {
		t.Error("want an error")
	}
}
