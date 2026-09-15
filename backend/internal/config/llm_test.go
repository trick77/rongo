package config

import (
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
	if cfg.LLMModel != "" || cfg.LLMGateModel != "" {
		t.Errorf("models = %q/%q, want empty (internal/llm's defaults)", cfg.LLMModel, cfg.LLMGateModel)
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
