// Command rongo serves the API and the embedded SPA.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/httpapi"
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

	srv := httpapi.NewServer(httpapi.Deps{})

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
