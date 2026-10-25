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

func TestRegistry_deepAndFlatAreTheChatProfileWithTheirOwnDefault(t *testing.T) {
	a, err := Registry().Require(Answer, llmwire.Needs{Tools: true, Streaming: true})
	if err != nil {
		t.Fatal(err)
	}
	for id, noKnob := range map[string]int{Deep: DeepNoKnobOverhead, Flat: FlatNoKnobOverhead} {
		d, err := Registry().Require(id, llmwire.Needs{Tools: true, Streaming: true})
		if err != nil {
			t.Fatal(err)
		}
		if d.Provider != a.Provider || d.Reasoning.Balanced != a.Reasoning.Balanced {
			t.Errorf("%s = %s/%q, want the answer's provider and balanced level", id, d.Provider, d.Reasoning.Balanced)
		}
		if got := d.Reasoning.Overhead["default"]; got != noKnob {
			t.Errorf("%s no-knob overhead = %d, want %d", id, got, noKnob)
		}
		if d.Limits.MaxOutput != DeepMaxOutput {
			t.Errorf("%s max output = %d, want %d", id, d.Limits.MaxOutput, DeepMaxOutput)
		}
	}
	if got := a.Reasoning.Overhead[a.Reasoning.Balanced]; got != FlatNoKnobOverhead {
		t.Errorf("balanced overhead = %d, want Flat's no-knob %d", got, FlatNoKnobOverhead)
	}
}

func TestWithClones_refusesADocumentWithoutTheChatProfile(t *testing.T) {
	for _, doc := range []string{
		"providers: {}\nprofiles: []\n",
		"a: 1\n",
		"providers: {}\nprofiles: [{id: " + Answer + ", provider: x}]\n",
		"providers: {}\nprofiles: [{id: " + Answer + ", provider: x, reasoning: {}, limits: {}}]\n",
	} {
		if _, err := withClones([]byte(doc)); err == nil {
			t.Errorf("%q: want an error", doc)
		}
	}
}
