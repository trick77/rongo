// Package config loads rongo's runtime configuration from environment
// variables. Every setting is BACKEND_*; secrets come from the environment only.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/trick77/rongo/internal/indexer"
)

// AuthMode selects how rongo identifies a caller.
type AuthMode string

const (
	// AuthModeDev auto-logs in a fixed admin. Loopback addresses only.
	AuthModeDev AuthMode = "dev"
	// AuthModeToken gates every request on a shared bearer token.
	AuthModeToken AuthMode = "token"
	// AuthModeOIDC is the production mode. The seam exists in phase 1; the
	// implementation lands later.
	AuthModeOIDC AuthMode = "oidc"
)

// Config holds all runtime settings.
type Config struct {
	Addr     string // HTTP listen address
	DBPath   string // path to the single SQLite file
	RepoRoot string // where rongo clones the repositories it indexes
	// ReposFile is the path to the repository list. Its entries carry no
	// secrets: tokens are named by token_env and read from the environment,
	// because that file ends up in a repository or a ticket eventually.
	ReposFile string
	// IndexMaxFileBytes is the ceiling above which a file is skipped WHOLE
	// rather than truncated: half a file produces confidently wrong answers
	// about the other half.
	IndexMaxFileBytes int
	// IndexEnabled switches the whole indexing side off, for a deployment that
	// only serves the UI. It defaults to ON, and while it is on an embedding
	// endpoint is mandatory: an indexer that cannot embed produces a repository
	// list that looks configured and an index that stays empty.
	IndexEnabled bool
	// IndexComments keeps whole-line comments in the text that is embedded and
	// full-text indexed. Setting BACKEND_INDEX_COMMENTS=0 leaves only code in
	// the search lanes; the source itself is stored untouched either way, so a
	// citation always quotes the real file.
	//
	// Changing this changes every chunk's content hash, so flipping it costs a
	// full re-embed of the corpus. That is intended: reusing vectors computed
	// with comments under a setting that excludes them would be silently wrong.
	IndexComments bool
	// IndexExclude lists repo-relative path globs whose files are recorded as
	// skipped ("excluded") instead of embedded: content written for reading,
	// not for the corpus — design documents, plans, mock-ups — that is stale
	// or wrong as an answer to how the code works. "**" spans directories;
	// patterns are anchored at the repository root. BACKEND_INDEX_EXCLUDE,
	// comma-separated; unset or empty is the default, "none" excludes nothing.
	// Already-indexed matches are swept at the next start; removing a pattern
	// takes effect for a file when it next changes.
	IndexExclude []string
	// ModuleMinChunks and ModuleMaxChunks are the module cut: below the first a
	// directory is folded into its parent, above the second it is split again.
	// Calibrated against the real corpus and recorded in the measurement
	// document — the defaults here are a starting point, not a finding.
	ModuleMinChunks int
	ModuleMaxChunks int
	// RouteMargin is how far the leading candidate must be ahead before a
	// turn answers without asking. The phase 4b routing measurement
	// (docs/measurements/2026-08-18-routing.md) swept this value and found
	// that LOWER margins score better on its accuracy table — but only
	// because asking less scores better on a catalogue that is 80% "do not
	// ask", and the best-scoring value (0.10) would optimise the router
	// towards switching itself off. The default stays at 0.25 despite the
	// sweep, pending a fix to the candidate layer (phase 4c). The number to
	// beat is 0.803: a router that never asks anything at all.
	RouteMargin float64
	// The MiMo endpoint. The two deployment NAMES are hardcoded in
	// internal/llm and deliberately not settings: a deployment name in the
	// environment lets a misconfigured host answer with a model nobody chose.
	LLMBaseURL string
	LLMAPIKey  string
	// GatherMaxHops and GatherTokenBudget bound the reference walk. Without
	// them one question walks the corpus.
	GatherMaxHops     int
	GatherTokenBudget int
	// Embedding endpoint. EmbedDim is also the width the vec0 table is built
	// with, so changing it means a new database, not a restart — store.BuiltDim
	// makes a mismatch a loud failure rather than a wrong answer.
	EmbedBaseURL  string
	EmbedAPIKey   string
	EmbedModel    string
	EmbedDim      int
	AuthMode      AuthMode
	AdminToken    string // required when AuthMode is token
	SessionSecret string // reserved: not read by anything yet — see the check below
	LogLevel      string
	// The OIDC block, all required when AuthMode is oidc. The issuer carries no
	// path and no trailing slash for Authelia (https://auth.trick77.com);
	// anything else fails discovery.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	// OIDCAdminGroup is the group whose members are admins. Empty means no
	// group check: everyone the provider let through is an admin, which is the
	// truthful default while the only real gate is Authelia's
	// authorization_policy.
	OIDCAdminGroup string
	// CookieSecure marks the session and nonce cookies Secure. Derived from
	// OIDCRedirectURL: behind a TLS-terminating proxy the process only ever
	// sees plain HTTP, and the redirect URL is the one setting that has to
	// name the external origin anyway.
	CookieSecure bool
}

// Load reads and validates the environment. It returns the first problem it
// finds rather than starting a half-configured server.
func Load() (Config, error) {
	// The embedding dimension is the one integer setting that may NOT fall back
	// silently: it is baked into the vec0 table when the database is created,
	// so a typo ("3O72" with a letter O) would build a 1536-wide table while
	// the operator believes it is 3072, and the mistake surfaces much later as
	// a per-request dimension mismatch.
	embedDim := 1536
	if v := strings.TrimSpace(os.Getenv("BACKEND_EMBED_DIM")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf(
				"BACKEND_EMBED_DIM = %q is not a positive number of dimensions; it is baked into the vector table when the database is created, so it must not be guessed", v)
		}
		embedDim = n
	}

	cfg := Config{
		Addr:      envOr("BACKEND_ADDR", "127.0.0.1:8080"),
		DBPath:    envOr("BACKEND_DB_PATH", "./data/rongo.db"),
		RepoRoot:  envOr("BACKEND_REPO_ROOT", "./repos"),
		ReposFile: envOr("BACKEND_REPOS_FILE", "./repos.yaml"),
		// 1 MiB. A source file above that is machine-written or a data blob,
		// not something a person asks how it works.
		IndexMaxFileBytes: envIntOr("BACKEND_INDEX_MAX_FILE_BYTES", 1<<20),
		IndexEnabled:      envBoolOr("BACKEND_INDEX_ENABLED", true),
		IndexComments:     envBoolOr("BACKEND_INDEX_COMMENTS", true),
		IndexExclude:      envListOr("BACKEND_INDEX_EXCLUDE", []string{"docs/plans/**"}),
		ModuleMinChunks:   envIntOr("BACKEND_MODULE_MIN_CHUNKS", 8),
		ModuleMaxChunks:   envIntOr("BACKEND_MODULE_MAX_CHUNKS", 150),
		RouteMargin:       envFloatOr("BACKEND_ROUTE_MARGIN", 0.25),
		LLMBaseURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("BACKEND_LLM_BASE_URL")), "/"),
		LLMAPIKey:         strings.TrimSpace(os.Getenv("BACKEND_LLM_API_KEY")),
		GatherMaxHops:     envIntOr("BACKEND_GATHER_MAX_HOPS", 2),
		GatherTokenBudget: envIntOr("BACKEND_GATHER_TOKEN_BUDGET", 24000),
		EmbedBaseURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("BACKEND_EMBED_BASE_URL")), "/"),
		EmbedAPIKey:       strings.TrimSpace(os.Getenv("BACKEND_EMBED_API_KEY")),
		EmbedModel:        envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"),
		EmbedDim:          embedDim,
		AuthMode:          AuthMode(envOr("BACKEND_AUTH_MODE", string(AuthModeDev))),
		AdminToken:        strings.TrimSpace(os.Getenv("BACKEND_ADMIN_TOKEN")),
		SessionSecret:     strings.TrimSpace(os.Getenv("BACKEND_SESSION_SECRET")),
		LogLevel:          envOr("BACKEND_LOG_LEVEL", "info"),
		// The issuer is trimmed of its trailing slash for the same reason the
		// endpoint URLs above are: a discovery URL built from
		// "https://auth.example.com/" gets a double slash and 404s.
		OIDCIssuer:       strings.TrimRight(strings.TrimSpace(os.Getenv("BACKEND_OIDC_ISSUER")), "/"),
		OIDCClientID:     strings.TrimSpace(os.Getenv("BACKEND_OIDC_CLIENT_ID")),
		OIDCClientSecret: strings.TrimSpace(os.Getenv("BACKEND_OIDC_CLIENT_SECRET")),
		OIDCRedirectURL:  strings.TrimSpace(os.Getenv("BACKEND_OIDC_REDIRECT_URL")),
		OIDCAdminGroup:   strings.TrimSpace(os.Getenv("BACKEND_OIDC_ADMIN_GROUP")),
	}

	// SessionSecret is currently unused — sessions are 256-bit random tokens
	// stored as unsalted SHA-256, no signing involved yet. It is still
	// required so a later phase that adds cookie signing can assume the
	// value is real instead of finding every deployment signed with a
	// placeholder. "change-me" and anything under 16 characters are rejected
	// for the same reason.
	if cfg.SessionSecret == "" {
		return Config{}, fmt.Errorf("BACKEND_SESSION_SECRET is required")
	}
	if cfg.SessionSecret == "change-me" {
		return Config{}, fmt.Errorf(
			"BACKEND_SESSION_SECRET must not be the placeholder value %q; generate one with `openssl rand -base64 32`", "change-me")
	}
	if len(cfg.SessionSecret) < 16 {
		return Config{}, fmt.Errorf(
			"BACKEND_SESSION_SECRET must be at least 16 characters; generate one with `openssl rand -base64 32`")
	}

	if cfg.IndexEnabled && cfg.EmbedBaseURL == "" {
		return Config{}, fmt.Errorf(
			"BACKEND_EMBED_BASE_URL is required while indexing is enabled; set it, or set BACKEND_INDEX_ENABLED=false to run without indexing")
	}
	if cfg.IndexEnabled && cfg.EmbedAPIKey == "" {
		return Config{}, fmt.Errorf(
			"BACKEND_EMBED_API_KEY is required while indexing is enabled; the endpoint at BACKEND_EMBED_BASE_URL authenticates with it and answers 401 without")
	}

	// Answering questions is what rongo is for. Without a model endpoint it
	// would start, index, and then answer every question with 503 — a
	// deployment that looks healthy and is useless. Both values are fatal.
	if cfg.LLMBaseURL == "" {
		return Config{}, fmt.Errorf("BACKEND_LLM_BASE_URL is required; without it no question can be answered")
	}
	if cfg.LLMAPIKey == "" {
		return Config{}, fmt.Errorf(
			"BACKEND_LLM_API_KEY is required; the endpoint at BACKEND_LLM_BASE_URL authenticates with it and answers 401 without")
	}

	switch cfg.AuthMode {
	case AuthModeDev:
		if !isLoopback(cfg.Addr) {
			return Config{}, fmt.Errorf(
				"BACKEND_AUTH_MODE=dev signs in an admin without credentials and is only allowed on a loopback address, got BACKEND_ADDR=%q", cfg.Addr)
		}
	case AuthModeToken:
		if cfg.AdminToken == "" {
			return Config{}, fmt.Errorf("BACKEND_AUTH_MODE=token requires BACKEND_ADMIN_TOKEN")
		}
	case AuthModeOIDC:
		// Every one of these is fatal rather than a warning. A half-configured
		// OIDC deployment starts, serves the SPA, and then refuses every login
		// with a callback error — which looks like a provider outage rather
		// than a missing environment variable.
		for _, m := range []struct {
			name  string
			value string
		}{
			{"BACKEND_OIDC_ISSUER", cfg.OIDCIssuer},
			{"BACKEND_OIDC_CLIENT_ID", cfg.OIDCClientID},
			{"BACKEND_OIDC_CLIENT_SECRET", cfg.OIDCClientSecret},
			{"BACKEND_OIDC_REDIRECT_URL", cfg.OIDCRedirectURL},
		} {
			if m.value == "" {
				return Config{}, fmt.Errorf("BACKEND_AUTH_MODE=oidc requires %s", m.name)
			}
		}
		// The session and the OIDC nonce cookies get their Secure flag from
		// the redirect URL alone. Behind a TLS-terminating proxy the process
		// only sees plain HTTP, so nothing else can notice that an operator
		// wrote http://; the login works and the cookies go out readable.
		if !strings.HasPrefix(strings.ToLower(cfg.OIDCRedirectURL), "https://") {
			return Config{}, fmt.Errorf(
				"BACKEND_AUTH_MODE=oidc requires an https BACKEND_OIDC_REDIRECT_URL, got %q; the session cookie's Secure flag is derived from it", cfg.OIDCRedirectURL)
		}
	default:
		return Config{}, fmt.Errorf("unknown BACKEND_AUTH_MODE %q (want dev, token or oidc)", cfg.AuthMode)
	}

	cfg.CookieSecure = strings.HasPrefix(strings.ToLower(cfg.OIDCRedirectURL), "https://")

	// A malformed exclusion pattern fails the boot: silently matching nothing
	// would keep the excluded content in the index while the setting looked
	// right.
	if err := indexer.ValidateExclude(cfg.IndexExclude); err != nil {
		return Config{}, fmt.Errorf("BACKEND_INDEX_EXCLUDE: %w", err)
	}

	return cfg, nil
}

// envListOr reads a comma-separated list. Unset or blank means the default,
// like every other setting; the literal "none" is how an operator switches
// the list off, since an empty value cannot say "nothing" here.
func envListOr(key string, fallback []string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	if strings.EqualFold(v, "none") {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// envIntOr reads a positive integer setting. A malformed or non-positive value
// falls back to the default rather than failing the boot: an indexing tunable
// is not worth refusing to start over, and the value is logged at debug level
// by the caller if it matters.
func envIntOr(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// envFloatOr reads a positive float setting. A malformed or non-positive
// value falls back to the default rather than failing the boot, for the same
// reason envIntOr does.
func envFloatOr(key string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return fallback
	}
	return f
}

// envBoolOr reads an on/off setting. Anything unrecognised falls back to the
// default rather than failing the boot.
func envBoolOr(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// isLoopback reports whether addr's host resolves to a loopback address. An
// empty host (":8080") means every interface and is therefore not loopback.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
