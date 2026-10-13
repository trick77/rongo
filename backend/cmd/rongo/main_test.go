package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/llmwire"
	"github.com/trick77/llmwire/llmwiretest"

	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/llm/llmtest"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/store/storetest"
)

// The container healthcheck is what Traefik routes on, and Docker never
// restarts an unhealthy container: it must report readiness, so a process
// that serves but cannot reach its database leaves rotation.
func TestHealthcheck_probesReadiness(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ready := http.StatusServiceUnavailable
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(ready) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")

	if got := healthcheck(addr); got != 1 {
		t.Errorf("healthcheck with /readyz failing = %d, want 1", got)
	}
	ready = http.StatusOK
	if got := healthcheck(addr); got != 0 {
		t.Errorf("healthcheck with /readyz passing = %d, want 0", got)
	}
}

// kit's OIDC defaults are not rongo's: its transient cookies must carry the
// app prefix, or a sibling app on the parent domain overwrites a half-finished
// login, and the scopes must not ask for groups, which an Authelia client
// without that scope refuses outright.
func TestOIDCConfig_keepsRongosCookiesAndScopes(t *testing.T) {
	got := oidcConfig(config.Config{OIDCIssuer: "https://auth.example", OIDCClientID: "rongo",
		OIDCClientSecret: "s", OIDCRedirectURL: "https://rongo.example/api/auth/callback", CookieSecure: true})

	if got.CookiePrefix != "rongo_" {
		t.Errorf("CookiePrefix = %q, want rongo_", got.CookiePrefix)
	}
	if strings.Join(got.Scopes, " ") != "openid profile email" {
		t.Errorf("Scopes = %v, want openid profile email", got.Scopes)
	}
	if !got.Secure || got.Group != "" || got.Issuer != "https://auth.example" || got.ClientID != "rongo" ||
		got.ClientSecret != "s" || got.RedirectURL != "https://rongo.example/api/auth/callback" {
		t.Errorf("config = %+v, want the environment's values, Secure, no login group", got)
	}
}

// The hosts are llmwire's profiles' and the keys are llmwire's to read from
// the environment; what main owns is refusing to boot, with the variable
// named, when a key is missing. The chat lanes run on synthetic models; the
// embedder is a constant of the build and reads the real environment.
func TestNewModelClients_namesTheMissingVariable(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	cfg := config.Config{LLMModel: llmtest.Answer, LLMGateModel: llmtest.Answer}
	chat := llm.Config{Registry: llmtest.Registry(), Lookup: srv.Lookup}
	ep, err := llmwire.Default().LookupEmbedding(embed.Model)
	if err != nil {
		t.Fatal(err)
	}
	embedKey := ep.APIKeyEnv()

	t.Setenv(embedKey, "x")
	if _, _, err := newModelClients(cfg, chat); err != nil {
		t.Fatalf("both keys set: %v", err)
	}

	t.Run(embedKey, func(t *testing.T) {
		t.Setenv(embedKey, "")
		_, _, err := newModelClients(cfg, chat)
		var me *llmwire.MissingEnvError
		if !errors.As(err, &me) || me.Var != embedKey {
			t.Fatalf("got %v, want a MissingEnvError naming %s", err, embedKey)
		}
	})

	t.Run("chat key", func(t *testing.T) {
		const key = "LLMWIRE_LLMWIRETEST_API_KEY"
		noKey := chat
		noKey.Lookup = func(name string) (string, bool) {
			if name == key {
				return "", false
			}
			return srv.Lookup(name)
		}
		_, _, err := newModelClients(cfg, noKey)
		var me *llmwire.MissingEnvError
		if !errors.As(err, &me) || me.Var != key {
			t.Fatalf("got %v, want a MissingEnvError naming %s", err, key)
		}
	})
}

// BACKEND_LLM_REASONING reaches the answer lane, and a level the answer
// model's profile does not list stops the boot naming the accepted ones.
func TestNewModelClients_checksTheAnswerReasoningLevel(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	chat := llm.Config{Registry: llmtest.Registry(), Lookup: srv.Lookup}
	ep, err := llmwire.Default().LookupEmbedding(embed.Model)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(ep.APIKeyEnv(), "x")

	cfg := config.Config{LLMModel: llmtest.Answer, LLMGateModel: llmtest.Answer, LLMReasoning: "high"}
	if _, _, err := newModelClients(cfg, chat); err != nil {
		t.Fatalf("an accepted level: %v", err)
	}

	cfg.LLMReasoning = "ultra"
	_, _, err = newModelClients(cfg, chat)
	if err == nil || !strings.Contains(err.Error(), "BACKEND_LLM_REASONING") || !strings.Contains(err.Error(), "none, low, medium, high") {
		t.Fatalf("err = %v, want a refusal naming BACKEND_LLM_REASONING and the accepted levels", err)
	}
}

// The vec0 table is created at embed.Model's width, and a file created at
// another width is refused rather than written into.
func TestMigrateForModel_refusesAFileBuiltForAnotherWidth(t *testing.T) {
	fresh, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := migrateForModel(fresh); err != nil {
		t.Fatalf("a fresh file: %v", err)
	}
	if built, _ := store.BuiltDim(fresh); built != embed.Dim() {
		t.Fatalf("built %d, want %d", built, embed.Dim())
	}

	other := storetest.Open(t, embed.Dim()+1)
	err = migrateForModel(other)
	if err == nil || !strings.Contains(err.Error(), embed.Model) {
		t.Fatalf("got %v, want a refusal naming the model", err)
	}
}

// Both failures on the way to the width are reported as such, never as a
// width mismatch.
func TestMigrateForModel_namesTheStepThatFailed(t *testing.T) {
	closed, err := store.Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if err := migrateForModel(closed); err == nil || !strings.Contains(err.Error(), "apply migrations") {
		t.Fatalf("closed database: got %v, want the migration step named", err)
	}

	gone, err := store.Open(filepath.Join(t.TempDir(), "gone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer gone.Close()
	if err := migrateForModel(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := gone.Exec(`DROP TABLE chunks_vec`); err != nil {
		t.Fatal(err)
	}
	if err := migrateForModel(gone); err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("vector table gone: got %v, want the read step named", err)
	}
}
