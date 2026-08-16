package config

import "testing"

// validSecret satisfies the length and placeholder checks so tests that don't
// care about SessionSecret validation itself don't trip over it.
const validSecret = "s3cret-long-enough"

// allBackendEnvVars lists every BACKEND_* variable Load reads. Tests clear
// all of them before setting their own, so a developer with e.g. BACKEND_ADDR
// exported in their shell doesn't fail an unrelated test.
var allBackendEnvVars = []string{
	"BACKEND_ADDR",
	"BACKEND_PUBLIC_URL",
	"BACKEND_DB_PATH",
	"BACKEND_REPO_ROOT",
	"BACKEND_AUTH_MODE",
	"BACKEND_ADMIN_TOKEN",
	"BACKEND_SESSION_SECRET",
	"BACKEND_LOG_LEVEL",
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range allBackendEnvVars {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoad_appliesDefaults(t *testing.T) {
	// Given
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.Addr != "127.0.0.1:8080" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, "127.0.0.1:8080")
	}
	if cfg.DBPath != "./data/rongo.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "./data/rongo.db")
	}
	if cfg.RepoRoot != "./repos" {
		t.Errorf("RepoRoot = %q, want %q", cfg.RepoRoot, "./repos")
	}
	if cfg.AuthMode != AuthModeDev {
		t.Errorf("AuthMode = %q, want %q", cfg.AuthMode, AuthModeDev)
	}
}

func TestLoad_requiresSessionSecret(t *testing.T) {
	setEnv(t, map[string]string{"BACKEND_SESSION_SECRET": ""})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want an error about BACKEND_SESSION_SECRET")
	}
}

func TestLoad_rejectsPlaceholderSessionSecret(t *testing.T) {
	// Given: "change-me" is the value .env.example must never ship as
	// something the loader accepts — a later phase that signs cookies with it
	// would sign every deployment with the same public placeholder.
	setEnv(t, map[string]string{"BACKEND_SESSION_SECRET": "change-me"})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal of the literal placeholder \"change-me\"")
	}
}

func TestLoad_rejectsShortSessionSecret(t *testing.T) {
	setEnv(t, map[string]string{"BACKEND_SESSION_SECRET": "short"})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal of a secret under 16 characters")
	}
}

func TestLoad_devModeRefusesNonLoopbackAddr(t *testing.T) {
	// Given: dev mode auto-logs in an admin. Exposing that on 0.0.0.0 is an
	// open door, so the config layer refuses it rather than trusting operators.
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_AUTH_MODE":      "dev",
		"BACKEND_ADDR":           "0.0.0.0:8080",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal to run dev auth on a non-loopback address")
	}
}

func TestLoad_tokenModeRequiresAdminToken(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_AUTH_MODE":      "token",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want an error about BACKEND_ADMIN_TOKEN")
	}
}

func TestLoad_rejectsUnknownAuthMode(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_AUTH_MODE":      "kerberos",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want an error about an unknown auth mode")
	}
}
