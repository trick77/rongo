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

	"github.com/trick77/rongo/internal/ask"
	"github.com/trick77/rongo/internal/auth"
	"github.com/trick77/rongo/internal/config"
	"github.com/trick77/rongo/internal/embed"
	"github.com/trick77/rongo/internal/exttools"
	"github.com/trick77/rongo/internal/gitrepo"
	"github.com/trick77/rongo/internal/httpapi"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/repostatus"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/sourceview"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/symbols"
	"github.com/trick77/rongo/internal/threads"
)

// sweepExcluded applies BACKEND_INDEX_EXCLUDE to every repository's existing
// index and refreshes the Repos page totals where it removed something.
func sweepExcluded(ctx context.Context, state *indexer.StateStore, pipeline *indexer.Indexer) {
	all, err := state.All(ctx)
	if err != nil {
		slog.Warn("exclusion sweep skipped; repository list unreadable", "err", err)
		return
	}
	for _, st := range all {
		changed, counts, err := pipeline.SweepExcluded(ctx, st.Name)
		if err != nil {
			slog.Warn("exclusion sweep failed", "repo", st.Name, "err", err)
			continue
		}
		if changed == 0 {
			continue
		}
		if err := state.SetCounts(ctx, st.Name, counts); err != nil {
			slog.Warn("recording the swept totals failed", "repo", st.Name, "err", err)
		}
		slog.Info("excluded files removed from the index", "repo", st.Name, "files", changed)
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Logging is not configured yet, so this goes to stderr directly.
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	// The pattern syntax belongs to the indexer, so the check lives there and
	// config stays a stdlib-only leaf. A malformed pattern fails the boot:
	// silently matching nothing would keep the excluded content in the index
	// while the setting looked right.
	if err := indexer.ValidateExclude(cfg.IndexExclude); err != nil {
		fmt.Fprintf(os.Stderr, "config: BACKEND_INDEX_EXCLUDE: %v\n", err)
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

	gitClient := gitrepo.New(tools.Git, cfg.RepoRoot)
	pipeline := indexer.New(indexer.Deps{
		DB:      db,
		Git:     gitClient,
		Symbols: symbols.NewExtractor(tools.Ctags),
		Embedder: embed.NewClient(embed.Config{
			BaseURL: cfg.EmbedBaseURL,
			APIKey:  cfg.EmbedAPIKey,
			Model:   cfg.EmbedModel,
			Dim:     cfg.EmbedDim,
		}, nil),
		Cache:  embed.NewCache(db, cfg.EmbedModel, cfg.EmbedDim),
		Writer: indexer.NewWriter(db),
		Selector: indexer.NewSelector(indexer.SelectOptions{
			MaxBytes: cfg.IndexMaxFileBytes,
			Exclude:  cfg.IndexExclude,
		}),
		Chunk: chunkOptions(cfg),
	})

	poller := indexer.NewPoller(indexer.PollerDeps{
		State: state,
		Git:   gitClient,
		Index: pipeline.IndexRepo,
		// Tokens are read from the environment by the variable name the YAML
		// entry declared. The value never appears in repos.yaml.
		Tokens: func(tokenEnv string) string { return os.Getenv(tokenEnv) },
	})

	// Indexing can be switched off for a deployment that only serves the UI.
	// The server still comes up and the Repos page still shows what is
	// configured; nothing is fetched or embedded.
	pollCtx, stopPolling := context.WithCancel(ctx)
	defer stopPolling()
	var workers sync.WaitGroup
	if cfg.IndexEnabled {
		// The exclusion list is read at start, and nothing else revisits files
		// an earlier run already embedded: an incremental run touches only
		// changed paths, and the poller idles while HEAD is unchanged. So the
		// list is applied to the existing index here, once per start. A
		// failure is logged, not fatal: the next poll still indexes correctly.
		// It runs on the poller's goroutine so the HTTP side comes up without
		// waiting for it, and so a shutdown cancels it like any other index work.
		workers.Add(1)
		go func() {
			defer workers.Done()
			sweepExcluded(pollCtx, state, pipeline)
			poller.Run(pollCtx)
		}()
	} else {
		slog.Warn("indexing is disabled; no repository will be fetched or embedded",
			"fix", "set BACKEND_INDEX_ENABLED=true")
	}

	embedder := embed.NewClient(embed.Config{
		BaseURL: cfg.EmbedBaseURL,
		APIKey:  cfg.EmbedAPIKey,
		Model:   cfg.EmbedModel,
		Dim:     cfg.EmbedDim,
	}, nil)
	deps := httpapi.Deps{
		Auth:           authSvc,
		Repos:          repostatus.New(db, moduleOpts(cfg)),
		Threads:        threads.NewStore(db),
		Source:         sourceview.New(db, gitClient),
		OIDCAdminGroup: cfg.OIDCAdminGroup,
		CookieSecure:   cfg.CookieSecure,
	}
	// Discovery talks to the provider, so a rongo that cannot reach Authelia
	// fails here rather than coming up healthy and rejecting every login. The
	// timeout is its own: the boot context has no deadline, and an unreachable
	// provider would otherwise hang the process instead of reporting it.
	if cfg.AuthMode == config.AuthModeOIDC {
		discoverCtx, cancelDiscover := context.WithTimeout(ctx, 30*time.Second)
		oidcSvc, err := auth.NewOIDCServiceFromDiscovery(discoverCtx, auth.OIDCServiceConfig{
			Issuer:       cfg.OIDCIssuer,
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			RedirectURL:  cfg.OIDCRedirectURL,
			SecureCookie: cfg.CookieSecure,
		})
		cancelDiscover()
		if err != nil {
			slog.Error("oidc provider discovery failed", "issuer", cfg.OIDCIssuer, "err", err)
			os.Exit(1)
		}
		deps.OIDC = oidcSvc
	}
	// config.Load rejects an empty BACKEND_LLM_BASE_URL, so the pipeline is
	// always wired: a rongo that indexes but cannot answer is not a mode
	// anyone wants to be in by accident.
	models := llm.NewClient(llm.Config{
		BaseURL:     cfg.LLMBaseURL,
		APIKey:      cfg.LLMAPIKey,
		IdleTimeout: 90 * time.Second,
	}, nil)
	deps.Ask = ask.NewPipeline(
		models,
		retrieve.New(db, embedder),
		ask.NewGatherer(db, ask.GatherOptions{MaxHops: cfg.GatherMaxHops, TokenBudget: cfg.GatherTokenBudget}),
		ask.NewRouter(models, db, cfg.RouteMargin, moduleOpts(cfg)),
	)
	deps.Titler = func(ctx context.Context, question string, lang ask.Language) string {
		return ask.Title(ctx, models, question, lang)
	}
	srv := httpapi.NewServer(deps)

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

// chunkOptions applies the comment switch to the default sizing. Comments are
// kept unless BACKEND_INDEX_COMMENTS=0: the search lanes then carry code only,
// while chunks.raw_text keeps the untouched source for citations.
func chunkOptions(cfg config.Config) indexer.ChunkOptions {
	o := indexer.DefaultChunkOptions()
	o.StripComments = !cfg.IndexComments
	return o
}

// moduleOpts are the clustering constants. The Repos page and the routing layer
// must be given the same ones, or the count on the page describes a cut nobody
// searches against.
func moduleOpts(cfg config.Config) modules.Opts {
	return modules.Opts{MinChunks: cfg.ModuleMinChunks, MaxChunks: cfg.ModuleMaxChunks}
}
