// Package llmtest is the model registry rongo's tests build their clients on:
// llmwiretest's synthetic profiles plus a second chat model on a provider of
// its own, so a test can tell the lanes apart on the wire and put them on two
// hosts without naming any real model, wire spelling or price.
//
// Test-only: nothing outside a _test.go file imports it.
package llmtest

import (
	"fmt"
	"sync"

	"github.com/trick77/llmwire"
	"github.com/trick77/llmwire/llmwiretest"
	"go.yaml.in/yaml/v3"
)

const (
	// Answer is the answer lane's model: llmwiretest.ChatModel, so
	// llmwiretest's constants (MinimalSent, BalancedOverhead, ...) hold for it.
	Answer = llmwiretest.ChatModel
	// Gate is the gate lane's model: the same profile as Answer under another
	// id, on GateProvider.
	Gate = "rongotest-gate"
	// GateProvider is Gate's provider. It ships no host, so FromEnv needs
	// LLMWIRE_RONGOTEST_BASE_URL and LLMWIRE_RONGOTEST_API_KEY for it.
	GateProvider = "rongotest"
	// NoTools is a chat model that cannot call tools (llmwiretest.BudgetModel).
	NoTools = llmwiretest.BudgetModel
	// Embed is an embeddings model: not a chat model at all.
	Embed = llmwiretest.EmbedModel
	// Deep is Answer under another id on Answer's provider, with a large
	// no-knob reasoning allowance and room to spend it: the shape of a model
	// whose default thinks long.
	Deep = "rongotest-deep"
	// DeepNoKnobOverhead is Deep's reasoning.overhead for no knob ("default").
	DeepNoKnobOverhead = 6000
	// DeepMaxOutput is Deep's output limit.
	DeepMaxOutput = 65536
)

var (
	once sync.Once
	reg  *llmwire.Registry
)

// Registry holds llmwiretest's profiles, Gate and Deep. Shared and read-only.
func Registry() *llmwire.Registry {
	once.Do(func() {
		doc, err := withClones(llmwiretest.Profiles())
		if err == nil {
			reg, err = llmwire.NewRegistry(doc)
		}
		if err != nil {
			panic("llmtest: " + err.Error())
		}
	})
	return reg
}

// withClones clones the synthetic chat profile as Gate on GateProvider and as
// Deep on Answer's own provider.
func withClones(doc []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	top := root.Content[0]
	providers, profiles := field(top, "providers"), field(top, "profiles")
	if providers == nil || profiles == nil {
		return nil, fmt.Errorf("llmwiretest profiles lack providers or profiles")
	}
	providers.Content = append(providers.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: GateProvider},
		&yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle})
	for _, p := range profiles.Content {
		if id := field(p, "id"); id == nil || id.Value != Answer {
			continue
		}
		g, err := clone(p, Gate, "rongotest Gate")
		if err != nil {
			return nil, err
		}
		field(g, "provider").Value = GateProvider
		d, err := clone(p, Deep, "rongotest Deep")
		if err != nil {
			return nil, err
		}
		if err := deepen(d); err != nil {
			return nil, err
		}
		profiles.Content = append(profiles.Content, g, d)
		return yaml.Marshal(&root)
	}
	return nil, fmt.Errorf("llmwiretest profiles lack %s", Answer)
}

// clone copies profile node p under a new id and display name.
func clone(p *yaml.Node, id, displayName string) (*yaml.Node, error) {
	var c yaml.Node
	raw, err := yaml.Marshal(p)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	n := c.Content[0]
	field(n, "id").Value = id
	if dn := field(n, "display_name"); dn != nil {
		dn.Value = displayName
	}
	return n, nil
}

// deepen gives Deep its no-knob allowance and output limit.
func deepen(d *yaml.Node) error {
	reasoning, limits := field(d, "reasoning"), field(d, "limits")
	if reasoning == nil || limits == nil {
		return fmt.Errorf("llmwiretest %s lacks reasoning or limits", Answer)
	}
	overhead, maxOutput := field(reasoning, "overhead"), field(limits, "max_output")
	if overhead == nil || maxOutput == nil {
		return fmt.Errorf("llmwiretest %s lacks reasoning.overhead or limits.max_output", Answer)
	}
	overhead.Content = append(overhead.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "default"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(DeepNoKnobOverhead)})
	maxOutput.Value = fmt.Sprint(DeepMaxOutput)
	return nil
}

// field is the value node under key in a mapping node, or nil.
func field(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
