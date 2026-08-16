// Command rongo serves the API and the embedded SPA.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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

	healthcheck := flag.Bool("healthcheck", false, "probe /healthz and exit; used by the container healthcheck")
	flag.Parse()
	if *healthcheck {
		resp, err := http.Get("http://" + cfg.Addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
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

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv,
		// Reap connections that dawdle on headers or sit idle, e.g. a
		// misbehaving client or a scanner. Deliberately no WriteTimeout:
		// phase 4 streams SSE responses for minutes, and a global
		// WriteTimeout would cut those streams mid-response. Do not add one
		// here — per-handler deadlines, if ever needed, belong at the
		// handler level instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "auth_mode", string(cfg.AuthMode))
		serveErr <- httpServer.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "err", err)
			os.Exit(1)
		}
	case sig := <-sigCh:
		slog.Info("shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
			os.Exit(1)
		}
	}
}

// parseLevel maps BACKEND_LOG_LEVEL onto slog levels, defaulting to info.
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
