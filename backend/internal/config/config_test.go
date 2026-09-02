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
	"BACKEND_INDEX_ENABLED",
	"BACKEND_INDEX_MAX_FILE_BYTES",
	"BACKEND_REPOS_FILE",
	"BACKEND_EMBED_BASE_URL",
	"BACKEND_EMBED_API_KEY",
	"BACKEND_EMBED_MODEL",
	"BACKEND_EMBED_DIM",
	"BACKEND_ROUTE_MARGIN",
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
		"BACKEND_EMBED_BASE_URL": "http://embeddings.invalid/v1",
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.EmbedModel != "text-embedding-3-small" || cfg.EmbedDim != 1536 {
		t.Errorf("embedding defaults = %s/%d, want text-embedding-3-small/1536", cfg.EmbedModel, cfg.EmbedDim)
	}
	if !cfg.IndexEnabled {
		t.Error("IndexEnabled = false, want indexing on by default")
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

func TestLoad_requiresAnEmbeddingEndpointWhileIndexing(t *testing.T) {
	// Given: indexing on, no endpoint. An indexer that cannot embed leaves a
	// repository list that looks configured and an index that stays empty.
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
	})

	// When
	_, err := Load()

	// Then
	if err == nil {
		t.Fatal("Load() err = nil, want a demand for BACKEND_EMBED_BASE_URL")
	}
}

func TestLoad_allowsNoEmbeddingEndpointWhenIndexingIsOff(t *testing.T) {
	// Given: the escape hatch for a deployment that only serves the UI.
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_INDEX_ENABLED":  "false",
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.IndexEnabled {
		t.Error("IndexEnabled = true, want it off")
	}
}

func TestLoad_rejectsAMalformedEmbedDim(t *testing.T) {
	// Given: the dimension is baked into the vec0 table when the database is
	// created. Falling back the way every other integer setting does would let
	// "3O72" (letter O) build a 1536-wide table while the operator believes it
	// is 3072 — and the mistake would only surface much later, as a dimension
	// mismatch on every insert.
	for _, bad := range []string{"0", "-1", "3O72", "big"} {
		setEnv(t, map[string]string{
			"BACKEND_SESSION_SECRET": validSecret,
			"BACKEND_EMBED_BASE_URL": "http://embeddings.invalid/v1",
			"BACKEND_EMBED_DIM":      bad,
		})

		// When
		_, err := Load()

		// Then
		if err == nil {
			t.Errorf("Load() err = nil for BACKEND_EMBED_DIM=%q, want a refusal", bad)
		}
	}
}

func TestLoad_acceptsAnExplicitEmbedDim(t *testing.T) {
	// Given
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_EMBED_BASE_URL": "http://embeddings.invalid/v1",
		"BACKEND_EMBED_DIM":      "3072",
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.EmbedDim != 3072 {
		t.Errorf("EmbedDim = %d, want 3072", cfg.EmbedDim)
	}
}

func TestLoad_appliesRouteMarginDefault(t *testing.T) {
	// Given
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_EMBED_BASE_URL": "http://embeddings.invalid/v1",
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.RouteMargin != 0.25 {
		t.Errorf("RouteMargin = %v, want 0.25", cfg.RouteMargin)
	}
}

func TestLoad_acceptsAnExplicitRouteMargin(t *testing.T) {
	// Given
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_EMBED_BASE_URL": "http://embeddings.invalid/v1",
		"BACKEND_ROUTE_MARGIN":   "0.4",
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.RouteMargin != 0.4 {
		t.Errorf("RouteMargin = %v, want 0.4", cfg.RouteMargin)
	}
}

func TestLoad_trimsAdminToken(t *testing.T) {
	// Given: a token picked up with a trailing newline (e.g. from `echo` into
	// an env file) must authenticate the same as one without, or every
	// correct-looking Bearer request gets a silent 401.
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_EMBED_BASE_URL": "http://embeddings.invalid/v1",
		"BACKEND_AUTH_MODE":      "token",
		"BACKEND_ADMIN_TOKEN":    "s3cret-token\n",
	})

	cfg, err := Load()

	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.AdminToken != "s3cret-token" {
		t.Errorf("AdminToken = %q, want %q", cfg.AdminToken, "s3cret-token")
	}
}

func TestLoad_rejectsWhitespaceOnlySessionSecret(t *testing.T) {
	// Given: 16 raw spaces satisfy the length check unless it operates on the
	// trimmed value.
	setEnv(t, map[string]string{"BACKEND_SESSION_SECRET": "                "})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal of a whitespace-only secret")
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

// A half-configured OIDC deployment would start, serve the SPA and then refuse
// every login with a callback error, which reads as a provider outage rather
// than a missing variable. Each of the four is fatal at boot instead.
func TestLoad_oidcModeRequiresTheWholeBlock(t *testing.T) {
	full := map[string]string{
		"BACKEND_SESSION_SECRET":     validSecret,
		"BACKEND_AUTH_MODE":          "oidc",
		"BACKEND_PUBLIC_URL":         "https://rongo.example.com",
		"BACKEND_EMBED_BASE_URL":     "https://api.example.com/v1",
		"BACKEND_OIDC_ISSUER":        "https://auth.example.com",
		"BACKEND_OIDC_CLIENT_ID":     "rongo",
		"BACKEND_OIDC_CLIENT_SECRET": "s3cret",
		"BACKEND_OIDC_REDIRECT_URL":  "https://rongo.example.com/api/auth/callback",
	}
	for _, missing := range []string{
		"BACKEND_OIDC_ISSUER",
		"BACKEND_OIDC_CLIENT_ID",
		"BACKEND_OIDC_CLIENT_SECRET",
		"BACKEND_OIDC_REDIRECT_URL",
	} {
		t.Run("without "+missing, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range full {
				env[k] = v
			}
			env[missing] = ""
			setEnv(t, env)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() err = nil, want an error about %s", missing)
			}
		})
	}

	t.Run("with the whole block", func(t *testing.T) {
		setEnv(t, full)

		cfg, err := Load()

		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.OIDCClientID != "rongo" {
			t.Errorf("OIDCClientID = %q, want %q", cfg.OIDCClientID, "rongo")
		}
	})
}

// Authelia's issuer is the bare origin. A trailing slash produces a double
// slash in the discovery URL, and discovery 404s.
func TestLoad_trimsTrailingSlashFromIssuer(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET":     validSecret,
		"BACKEND_AUTH_MODE":          "oidc",
		"BACKEND_PUBLIC_URL":         "https://rongo.example.com",
		"BACKEND_EMBED_BASE_URL":     "https://api.example.com/v1",
		"BACKEND_OIDC_ISSUER":        "https://auth.example.com/",
		"BACKEND_OIDC_CLIENT_ID":     "rongo",
		"BACKEND_OIDC_CLIENT_SECRET": "s3cret",
		"BACKEND_OIDC_REDIRECT_URL":  "https://rongo.example.com/api/auth/callback",
	})

	cfg, err := Load()

	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if cfg.OIDCIssuer != "https://auth.example.com" {
		t.Errorf("OIDCIssuer = %q, want it without the trailing slash", cfg.OIDCIssuer)
	}
}

// Behind a TLS-terminating proxy the process only ever sees plain HTTP, so
// nothing but this check can notice that the public URL says http:// — and the
// session and nonce cookies would go out without Secure while the login works.
func TestLoad_oidcModeRejectsAnHttpPublicURL(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET":     validSecret,
		"BACKEND_AUTH_MODE":          "oidc",
		"BACKEND_PUBLIC_URL":         "http://rongo.example.com",
		"BACKEND_EMBED_BASE_URL":     "https://api.example.com/v1",
		"BACKEND_OIDC_ISSUER":        "https://auth.example.com",
		"BACKEND_OIDC_CLIENT_ID":     "rongo",
		"BACKEND_OIDC_CLIENT_SECRET": "s3cret",
		"BACKEND_OIDC_REDIRECT_URL":  "https://rongo.example.com/api/auth/callback",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal to run OIDC on a non-https public URL")
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
