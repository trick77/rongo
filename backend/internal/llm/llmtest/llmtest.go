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
	"gopkg.in/yaml.v3"
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
)

var (
	once sync.Once
	reg  *llmwire.Registry
)

// Registry holds llmwiretest's profiles and Gate. Shared and read-only.
func Registry() *llmwire.Registry {
	once.Do(func() {
		doc, err := withGate(llmwiretest.Profiles())
		if err == nil {
			reg, err = llmwire.NewRegistry(doc)
		}
		if err != nil {
			panic("llmtest: " + err.Error())
		}
	})
	return reg
}

// withGate clones the synthetic chat profile as Gate on GateProvider.
func withGate(doc []byte) ([]byte, error) {
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
		var clone yaml.Node
		raw, err := yaml.Marshal(p)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(raw, &clone); err != nil {
			return nil, err
		}
		g := clone.Content[0]
		field(g, "id").Value = Gate
		field(g, "provider").Value = GateProvider
		if dn := field(g, "display_name"); dn != nil {
			dn.Value = "rongotest Gate"
		}
		profiles.Content = append(profiles.Content, g)
		return yaml.Marshal(&root)
	}
	return nil, fmt.Errorf("llmwiretest profiles lack %s", Answer)
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
