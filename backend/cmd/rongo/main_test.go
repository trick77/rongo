package main

import (
	"errors"
	"testing"

	"github.com/trick77/llmwire"
	"github.com/trick77/rongo/internal/config"
)

// The endpoints are llmwire's to read from the environment; what main owns is
// refusing to boot, with the variable named, when one is missing.
func TestNewModelClients_namesTheMissingVariable(t *testing.T) {
	vars := []string{"LLMWIRE_OPENAI_BASE_URL", "LLMWIRE_OPENAI_API_KEY", "LLMWIRE_MIMO_BASE_URL", "LLMWIRE_MIMO_API_KEY"}
	for _, v := range vars {
		t.Setenv(v, "x")
	}
	cfg := config.Config{}
	if _, _, err := newModelClients(cfg); err != nil {
		t.Fatalf("all four set: %v", err)
	}
	for _, v := range vars {
		t.Run(v, func(t *testing.T) {
			t.Setenv(v, "")
			_, _, err := newModelClients(cfg)
			var me *llmwire.MissingEnvError
			if !errors.As(err, &me) || me.Var != v {
				t.Fatalf("got %v, want a MissingEnvError naming %s", err, v)
			}
		})
	}
}
