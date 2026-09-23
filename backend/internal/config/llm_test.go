package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad_llmPolicyDefaults(t *testing.T) {
	setEnv(t, nil)

	cfg, err := Load()

	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if cfg.LLMGateTemperature == nil || *cfg.LLMGateTemperature != 0 {
		t.Errorf("LLMGateTemperature = %v, want 0", cfg.LLMGateTemperature)
	}
	if cfg.LLMGateReasoning != "off" || cfg.LLMReasoning != "default" || cfg.LLMTimeout != 15*time.Minute {
		t.Errorf("policy = %q/%q/%v, want off/default/15m", cfg.LLMGateReasoning, cfg.LLMReasoning, cfg.LLMTimeout)
	}
}

// rongo has no model of its own: both lanes are configured or it does not
// start, and the error names the variable.
func TestLoad_requiresBothModels(t *testing.T) {
	for _, name := range []string{"BACKEND_LLM_MODEL", "BACKEND_LLM_GATE_MODEL"} {
		t.Run(name, func(t *testing.T) {
			setEnv(t, map[string]string{name: " "})
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Load() err = %v, want one naming %s", err, name)
			}
		})
	}
}

func TestLoad_llmPolicyValues(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_LLM_GATE_TEMPERATURE": "default",
		"BACKEND_LLM_GATE_REASONING":   "low",
		"BACKEND_LLM_REASONING":        "medium",
		"BACKEND_LLM_TIMEOUT":          "90s",
	})

	cfg, err := Load()

	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if cfg.LLMGateTemperature != nil {
		t.Errorf("LLMGateTemperature = %v, want nil for default", *cfg.LLMGateTemperature)
	}
	if cfg.LLMGateReasoning != "low" || cfg.LLMReasoning != "medium" || cfg.LLMTimeout != 90*time.Second {
		t.Errorf("policy = %q/%q/%v", cfg.LLMGateReasoning, cfg.LLMReasoning, cfg.LLMTimeout)
	}
}

// A typo'd temperature would change every routing decision, and a bad
// duration every call: neither may quietly fall back.
func TestLoad_llmPolicyRefusesMalformed(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"temperature": {"BACKEND_LLM_GATE_TEMPERATURE": "zero"},
		"nan":         {"BACKEND_LLM_GATE_TEMPERATURE": "NaN"},
		"inf":         {"BACKEND_LLM_GATE_TEMPERATURE": "+Inf"},
		"negative":    {"BACKEND_LLM_GATE_TEMPERATURE": "-1"},
		"timeout":     {"BACKEND_LLM_TIMEOUT": "15"},
	} {
		t.Run(name, func(t *testing.T) {
			setEnv(t, env)
			if _, err := Load(); err == nil {
				t.Fatal("Load() err = nil, want a refusal")
			}
		})
	}
}
