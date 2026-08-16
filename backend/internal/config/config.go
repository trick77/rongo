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
	SessionSecret string
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

	if cfg.SessionSecret == "" {
		return Config{}, fmt.Errorf("BACKEND_SESSION_SECRET is required")
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
		// prepared, and the auth layer answers 501 until it exists.
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
