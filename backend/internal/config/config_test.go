package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validSecret satisfies the length and placeholder checks so tests that don't
// care about SessionSecret validation itself don't trip over it.
const validSecret = "s3cret-long-enough"

// allBackendEnvVars lists every BACKEND_* variable Load reads. Tests clear
// all of them before setting their own, so a developer with e.g. BACKEND_ADDR
// exported in their shell doesn't fail an unrelated test.
var allBackendEnvVars = []string{
	"BACKEND_ADDR",
	"BACKEND_DB_PATH",
	"BACKEND_REPO_ROOT",
	"BACKEND_AUTH_MODE",
	"BACKEND_ADMIN_TOKEN",
	"BACKEND_ADMIN_USER",
	"BACKEND_ADMIN_PASSWORD_HASH",
	"BACKEND_COOKIE_SECURE",
	"BACKEND_SESSION_SECRET",
	"BACKEND_LOG_LEVEL",
	"BACKEND_INDEX_ENABLED",
	"BACKEND_INDEX_MAX_FILE_BYTES",
	"BACKEND_INDEX_MAX_DATA_FILE_BYTES",
	"BACKEND_INDEX_COMMENTS",
	"BACKEND_INDEX_EXCLUDE",
	"BACKEND_REPOS_FILE",
	"BACKEND_FORGE_TOKEN_GITHUB",
	"BACKEND_FORGE_TOKEN_BITBUCKET",
	"BACKEND_GIT_SSH_KEY",
	"BACKEND_GIT_SSH_KNOWN_HOSTS",
	"BACKEND_GIT_CA_FILE",
	"LLMWIRE_OPENAI_BASE_URL",
	"LLMWIRE_OPENAI_API_KEY",
	"LLMWIRE_MIMO_BASE_URL",
	"LLMWIRE_MIMO_API_KEY",
	"BACKEND_MODULE_MIN_CHUNKS",
	"BACKEND_MODULE_MAX_CHUNKS",
	"BACKEND_ROUTE_MARGIN",
	"BACKEND_GATHER_MAX_HOPS",
	"BACKEND_GATHER_TOKEN_BUDGET",
	"BACKEND_TURN_MAX_TOKENS",
	"BACKEND_LLM_MODEL",
	"BACKEND_LLM_GATE_MODEL",
	"BACKEND_LLM_GATE_TEMPERATURE",
	"BACKEND_LLM_GATE_REASONING",
	"BACKEND_LLM_REASONING",
	"BACKEND_LLM_TIMEOUT",
	"BACKEND_OIDC_ISSUER",
	"BACKEND_OIDC_CLIENT_ID",
	"BACKEND_OIDC_CLIENT_SECRET",
	"BACKEND_OIDC_REDIRECT_URL",
	"BACKEND_OIDC_ADMIN_GROUP",
}

// mandatoryEnv is what .env.example leaves uncommented: the values nothing
// defaults. setEnv seeds them so a test about something else doesn't have to
// repeat them; a test about one of them overrides it with "".
var mandatoryEnv = map[string]string{
	"BACKEND_SESSION_SECRET": validSecret,
	"LLMWIRE_OPENAI_API_KEY": "embed-key",
	"LLMWIRE_MIMO_API_KEY":   "llm-key",
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range allBackendEnvVars {
		t.Setenv(k, "")
	}
	for k, v := range mandatoryEnv {
		t.Setenv(k, v)
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
	if cfg.TurnMaxTokens != 250000 {
		t.Errorf("TurnMaxTokens = %d, want 250000", cfg.TurnMaxTokens)
	}
}

func TestLoad_turnMaxTokens(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"a figure is kept", "500000", 500000},
		{"zero switches the ceiling off", "0", 0},
		{"a malformed value falls back", "lots", 250000},
		{"a negative value falls back", "-5", 250000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			setEnv(t, map[string]string{
				"BACKEND_SESSION_SECRET":  validSecret,
				"BACKEND_TURN_MAX_TOKENS": tc.env,
			})

			// When
			cfg, err := Load()

			// Then
			if err != nil {
				t.Fatalf("Load() err = %v, want nil", err)
			}
			if cfg.TurnMaxTokens != tc.want {
				t.Errorf("TurnMaxTokens = %d, want %d", cfg.TurnMaxTokens, tc.want)
			}
		})
	}
}

func TestLoad_indexMaxDataFileBytes(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{name: "unset means 8 KiB", env: "", want: 8192},
		{name: "a number is taken", env: "16384", want: 16384},
		{name: "zero falls back, there is no switching the ceiling off", env: "0", want: 8192},
		{name: "garbage falls back", env: "lots", want: 8192},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			setEnv(t, map[string]string{"BACKEND_INDEX_MAX_DATA_FILE_BYTES": tc.env})

			// When
			cfg, err := Load()

			// Then
			if err != nil {
				t.Fatalf("Load() err = %v, want nil", err)
			}
			if cfg.IndexMaxDataFileBytes != tc.want {
				t.Errorf("IndexMaxDataFileBytes = %d, want %d", cfg.IndexMaxDataFileBytes, tc.want)
			}
		})
	}
}

func TestLoad_indexExclude(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want []string
	}{
		{name: "unset means the design-document default", env: "", want: []string{"docs/plans/**"}},
		{name: "a list is split and trimmed", env: " a/** , b/*.html ,", want: []string{"a/**", "b/*.html"}},
		{name: "none switches exclusion off", env: "none", want: nil},
		{name: "NONE is none too", env: " None ", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			setEnv(t, map[string]string{"BACKEND_INDEX_EXCLUDE": tc.env})

			// When
			cfg, err := Load()

			// Then
			if err != nil {
				t.Fatalf("Load() err = %v, want nil", err)
			}
			if len(cfg.IndexExclude) != len(tc.want) {
				t.Fatalf("IndexExclude = %q, want %q", cfg.IndexExclude, tc.want)
			}
			for i := range tc.want {
				if cfg.IndexExclude[i] != tc.want[i] {
					t.Errorf("IndexExclude[%d] = %q, want %q", i, cfg.IndexExclude[i], tc.want[i])
				}
			}
		})
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

func TestLoad_appliesRouteMarginDefault(t *testing.T) {
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
	if cfg.RouteMargin != 0.25 {
		t.Errorf("RouteMargin = %v, want 0.25", cfg.RouteMargin)
	}
}

func TestLoad_acceptsAnExplicitRouteMargin(t *testing.T) {
	// Given
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
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

func TestLoad_proxyModeRefusesNonLoopbackAddr(t *testing.T) {
	// Given: proxy mode believes X-Forwarded-User. On 0.0.0.0 anyone who can
	// reach the port writes that header themselves.
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_AUTH_MODE":      "proxy",
		"BACKEND_ADDR":           "0.0.0.0:8080",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal to run proxy auth on a non-loopback address")
	}
}

func TestLoad_proxyModeAcceptsLoopback(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_AUTH_MODE":      "proxy",
		"BACKEND_ADDR":           "127.0.0.1:8080",
	})

	cfg, err := Load()

	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if cfg.AuthMode != AuthModeProxy {
		t.Errorf("AuthMode = %q, want %q", cfg.AuthMode, AuthModeProxy)
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
// nothing but this check can notice that the redirect URL says http:// — and
// the session and nonce cookies would go out without Secure while the login
// works.
func TestLoad_oidcModeRejectsAnHttpRedirectURL(t *testing.T) {
	setEnv(t, map[string]string{
		"BACKEND_AUTH_MODE":          "oidc",
		"BACKEND_ADDR":               "0.0.0.0:8080",
		"BACKEND_OIDC_ISSUER":        "https://auth.example.com",
		"BACKEND_OIDC_CLIENT_ID":     "rongo",
		"BACKEND_OIDC_CLIENT_SECRET": "s3cret",
		"BACKEND_OIDC_REDIRECT_URL":  "http://rongo.example.com/api/auth/callback",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal to run OIDC on a non-https redirect URL")
	}
}

// CookieSecure is what the whole https check exists for.
func TestLoad_derivesCookieSecureFromTheRedirectURL(t *testing.T) {
	// Given a complete oidc setup
	setEnv(t, map[string]string{
		"BACKEND_AUTH_MODE":          "oidc",
		"BACKEND_ADDR":               "0.0.0.0:8080",
		"BACKEND_OIDC_ISSUER":        "https://auth.example.com",
		"BACKEND_OIDC_CLIENT_ID":     "rongo",
		"BACKEND_OIDC_CLIENT_SECRET": "s3cret",
		"BACKEND_OIDC_REDIRECT_URL":  "https://rongo.example.com/api/auth/callback",
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if !cfg.CookieSecure {
		t.Error("CookieSecure = false, want true for an https redirect URL")
	}
}

// In dev mode there is no redirect URL and the browser talks plain HTTP to
// loopback; a Secure cookie would simply never come back.
func TestLoad_devModeLeavesCookieSecureOff(t *testing.T) {
	setEnv(t, map[string]string{})

	cfg, err := Load()

	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.CookieSecure {
		t.Error("CookieSecure = true, want false without an https redirect URL")
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

func TestLoad_gitAuthFilesMustExist(t *testing.T) {
	// Given: a key, a known_hosts and a CA that are all there. The check is
	// at boot rather than at the first ssh fetch, where a missing file reads
	// as one repository's error instead of the deployment's.
	dir := t.TempDir()
	for _, name := range []string{"key", "known_hosts", "ca.pem"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setEnv(t, map[string]string{
		"BACKEND_GIT_SSH_KEY":         filepath.Join(dir, "key"),
		"BACKEND_GIT_SSH_KNOWN_HOSTS": filepath.Join(dir, "known_hosts"),
		"BACKEND_GIT_CA_FILE":         filepath.Join(dir, "ca.pem"),
	})

	cfg, err := Load()

	if err != nil {
		t.Fatalf("Load() err = %v, want nil with every file present", err)
	}
	if cfg.GitSSHKey != filepath.Join(dir, "key") || cfg.GitCAFile != filepath.Join(dir, "ca.pem") {
		t.Errorf("git auth paths not carried: %+v", cfg)
	}

	// And: one of them gone fails the boot by name.
	setEnv(t, map[string]string{
		"BACKEND_GIT_SSH_KEY":         filepath.Join(dir, "key"),
		"BACKEND_GIT_SSH_KNOWN_HOSTS": filepath.Join(dir, "missing"),
	})

	_, err = Load()

	if err == nil || !strings.Contains(err.Error(), "BACKEND_GIT_SSH_KNOWN_HOSTS") {
		t.Fatalf("Load() err = %v, want a refusal naming BACKEND_GIT_SSH_KNOWN_HOSTS", err)
	}
}

func TestLoad_sshKeyAndKnownHostsComeTogether(t *testing.T) {
	// Given: a key alone. Host-key checking is strict against the named
	// file, so a key with no known_hosts fails every ssh remote later.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, map[string]string{
		"BACKEND_GIT_SSH_KEY": filepath.Join(dir, "key"),
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal of a key without known_hosts")
	}
}

func TestLoad_locateLoopIsOnByDefault(t *testing.T) {
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
	if cfg.LocateRounds != 3 {
		t.Errorf("LocateRounds = %d, want 3: look, narrow, confirm", cfg.LocateRounds)
	}
}

func TestLoad_locateLoopCanBeSwitchedOff(t *testing.T) {
	// Given
	setEnv(t, map[string]string{
		"BACKEND_SESSION_SECRET": validSecret,
		"BACKEND_LOCATE_ROUNDS":  "0",
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.LocateRounds != 0 {
		t.Errorf("LocateRounds = %d, want 0 when set off", cfg.LocateRounds)
	}
}
