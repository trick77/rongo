// Package config loads rongo's runtime configuration from environment
// variables. Every setting is BACKEND_*; secrets come from the environment only.
package config

import (
	"fmt"
	"net"
	"os"
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
	Addr          string // HTTP listen address
	PublicURL     string // externally reachable base URL
	DBPath        string // path to the single SQLite file
	RepoRoot      string // where rongo clones the repositories it indexes
	AuthMode      AuthMode
	AdminToken    string // required when AuthMode is token
	SessionSecret string // reserved: not read by anything yet — see the check below
	LogLevel      string
}

// Load reads and validates the environment. It returns the first problem it
// finds rather than starting a half-configured server.
func Load() (Config, error) {
	cfg := Config{
		Addr:          envOr("BACKEND_ADDR", "127.0.0.1:8080"),
		PublicURL:     envOr("BACKEND_PUBLIC_URL", "http://127.0.0.1:8080"),
		DBPath:        envOr("BACKEND_DB_PATH", "./data/rongo.db"),
		RepoRoot:      envOr("BACKEND_REPO_ROOT", "./repos"),
		AuthMode:      AuthMode(envOr("BACKEND_AUTH_MODE", string(AuthModeDev))),
		AdminToken:    os.Getenv("BACKEND_ADMIN_TOKEN"),
		SessionSecret: os.Getenv("BACKEND_SESSION_SECRET"),
		LogLevel:      envOr("BACKEND_LOG_LEVEL", "info"),
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
