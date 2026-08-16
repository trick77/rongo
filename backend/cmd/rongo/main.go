// Command rongo serves the API and the embedded SPA.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/exttools"
	"github.com/trick77/rongo/internal/httpapi"
	"github.com/trick77/rongo/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Logging is not configured yet, so this goes to stderr directly.
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	})))

	tools, err := exttools.Resolve()
	if err != nil {
		slog.Error("required external tool missing or wrong", "err", err)
		os.Exit(1)
	}
	slog.Info("external tools resolved", "git", tools.Git, "rg", tools.Rg, "ctags", tools.Ctags)

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		slog.Error("create data directory", "err", err)
		os.Exit(1)
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		slog.Error("apply migrations", "err", err)
		os.Exit(1)
	}

	authSvc := auth.NewService(db, string(cfg.AuthMode), cfg.AdminToken)
	srv := httpapi.NewServer(httpapi.Deps{Auth: authSvc})

	slog.Info("listening", "addr", cfg.Addr, "auth_mode", string(cfg.AuthMode))
	if err := http.ListenAndServe(cfg.Addr, srv); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// parseLevel maps RONGO_LOG_LEVEL onto slog levels, defaulting to info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
