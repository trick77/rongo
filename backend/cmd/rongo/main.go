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
	"sync"
	"syscall"
	"time"

	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/exttools"
	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/httpapi"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/repos"
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

	// ctx is the process-wide root: startup work and the background workers all
	// hang off it, so a shutdown cancels everything from one place.
	ctx := context.Background()

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
	if err := store.Migrate(db, cfg.EmbedDim); err != nil {
		slog.Error("apply migrations", "err", err)
		os.Exit(1)
	}
	// The vec0 table's width is fixed when the database is created. Pointing a
	// differently configured process at an existing file is a loud failure
	// here rather than a rejected insert on every chunk much later — and, worse,
	// a semantic lane that silently answers nothing.
	builtDim, err := store.BuiltDim(db)
	if err != nil {
		slog.Error("read the vector table's dimension", "err", err)
		os.Exit(1)
	}
	if builtDim != cfg.EmbedDim {
		slog.Error("this database was built for a different embedding model",
			"built_dim", builtDim, "configured_dim", cfg.EmbedDim,
			"fix", "point BACKEND_DB_PATH at a fresh file, or set BACKEND_EMBED_DIM back")
		os.Exit(1)
	}

	authSvc := auth.NewService(db, string(cfg.AuthMode), cfg.AdminToken)

	// The repository list is loaded on a best-effort basis. A missing or broken
	// repos.yaml must NOT stop the server: the operator needs the Repos page to
	// come up and tell them what is wrong with the file. Refusing to boot would
	// hide the diagnosis behind the very thing that failed.
	state := indexer.NewStateStore(db)
	if specs, err := repos.Load(cfg.ReposFile); err != nil {
		slog.Warn("repository list unavailable; indexing is idle until it is fixed",
			"path", cfg.ReposFile, "err", err)
	} else if err := state.SyncSpecs(ctx, specs); err != nil {
		slog.Error("recording the repository list failed", "err", err)
		os.Exit(1)
	} else {
		slog.Info("repository list loaded", "path", cfg.ReposFile, "entries", len(specs))
	}

	poller := indexer.NewPoller(indexer.PollerDeps{
		State: state,
		Git:   gitrepo.New(tools.Git, cfg.RepoRoot),
		// The indexing pipeline lands in a later task. Until then the poller
		// keeps checkouts current and records state, and reports that it
		// indexed nothing rather than pretending it did.
		Index: func(context.Context, indexer.RepoState, string, []string) (indexer.Counts, error) {
			return indexer.Counts{}, nil
		},
		// Tokens are read from the environment by the variable name the YAML
		// entry declared. The value never appears in repos.yaml.
		Tokens: func(tokenEnv string) string { return os.Getenv(tokenEnv) },
	})

	pollCtx, stopPolling := context.WithCancel(ctx)
	defer stopPolling()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		poller.Run(pollCtx)
	}()

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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
			os.Exit(1)
		}
	}

	// Stop the background workers and wait for them, so a shutdown cannot leave
	// a git command or a half-written transaction behind.
	stopPolling()
	workers.Wait()
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
