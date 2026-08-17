// Package config loads rongo's runtime configuration from environment
// variables. Every setting is BACKEND_*; secrets come from the environment only.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
	Addr      string // HTTP listen address
	PublicURL string // externally reachable base URL
	DBPath    string // path to the single SQLite file
	RepoRoot  string // where rongo clones the repositories it indexes
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
		PublicURL: envOr("BACKEND_PUBLIC_URL", "http://127.0.0.1:8080"),
		DBPath:    envOr("BACKEND_DB_PATH", "./data/rongo.db"),
		RepoRoot:  envOr("BACKEND_REPO_ROOT", "./repos"),
		ReposFile: envOr("BACKEND_REPOS_FILE", "./repos.yaml"),
		// 1 MiB. A source file above that is machine-written or a data blob,
		// not something a person asks how it works.
		IndexMaxFileBytes: envIntOr("BACKEND_INDEX_MAX_FILE_BYTES", 1<<20),
		IndexEnabled:      envBoolOr("BACKEND_INDEX_ENABLED", true),
		IndexComments:     envBoolOr("BACKEND_INDEX_COMMENTS", true),
		EmbedBaseURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("BACKEND_EMBED_BASE_URL")), "/"),
		EmbedAPIKey:       strings.TrimSpace(os.Getenv("BACKEND_EMBED_API_KEY")),
		EmbedModel:        envOr("BACKEND_EMBED_MODEL", "text-embedding-3-small"),
		EmbedDim:          embedDim,
		AuthMode:          AuthMode(envOr("BACKEND_AUTH_MODE", string(AuthModeDev))),
		AdminToken:        strings.TrimSpace(os.Getenv("BACKEND_ADMIN_TOKEN")),
		SessionSecret:     strings.TrimSpace(os.Getenv("BACKEND_SESSION_SECRET")),
		LogLevel:          envOr("BACKEND_LOG_LEVEL", "info"),
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
		// Wired in a later phase; the mode is accepted so deployments can be
		// prepared, and the auth layer answers 401 until it exists.
	default:
		return Config{}, fmt.Errorf("unknown BACKEND_AUTH_MODE %q (want dev, token or oidc)", cfg.AuthMode)
	}

	return cfg, nil
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
