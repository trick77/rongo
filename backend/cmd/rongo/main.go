// Command rongo serves the API and the embedded SPA.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
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
	"github.com/trick77/rongo/internal/pricing"
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
	// A title call cannot outlive the process that started it, so a thread
	// still waiting for one was orphaned by the last shutdown. Left pending it
	// would hold "New question" in its header for good.
	if err := threads.NewStore(db).SettleTitles(ctx); err != nil {
		slog.Error("settle orphaned thread titles", "err", err)
		os.Exit(1)
	}
	// Every thread needs an address before a single request is served: it is
	// what /thread/… and every /api/threads/… path is written in, and a thread
	// that predates the column has none. Fatal rather than best-effort — a
	// thread with an empty public_id is a row the rail can render and nothing
	// can open.
	if err := threads.NewStore(db).BackfillPublicIDs(ctx); err != nil {
		slog.Error("give existing threads an address", "err", err)
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

	// Built before the list is synced: a repository that left the list is purged
	// from the database, and its checkout has to go with it.
	gitClient := gitrepo.New(tools.Git, cfg.RepoRoot)

	// An INVALID repos.yaml stops the server. A MISSING one does not.
	//
	// The two used to be one case, and both only warned. That is a worse trade
	// than it looks, and it cost an evening to learn: a file that fails to parse
	// leaves the previous list in the database untouched, so rongo comes up
	// serving a complete, confident, arbitrarily stale corpus while indexing
	// sits idle. Nothing in the UI says the configuration was refused — the
	// Repos page cannot report a file it never loaded, it can only show what the
	// database still holds, which is the LAST good file. The symptom reaching
	// the operator was a `uses` arrow pointing the wrong way, nine hours after a
	// stray character on line one had frozen everything.
	//
	// So: a file that is present and wrong is an operator mistake that has to be
	// fixed now, and refusing to boot is the only signal that cannot be missed.
	// Under compose this restarts in a loop, which is loud, which is the point.
	//
	// A file that is ABSENT is a different fact — a first run before conf/ has
	// been populated, or a mount that is not there yet. rongo comes up, because
	// an existing index still answers questions and taking the server down over
	// a mount that has not appeared yet would be the worse trade. But it does
	// NOT poll: the poller reads state.Active() out of the DATABASE, so with a
	// populated one it would happily go on fetching the PREVIOUS list, refreshing
	// last_run_at, and reporting "Index current" for a configuration nobody can
	// see — the exact deception this whole change exists to remove, just reached
	// by the other door. Not polling is what makes "indexing is idle" true, and
	// a frozen last_run_at is then the honest signal on the Repos page.
	//
	// listLoaded gates that below. It is the single fact the rest of the boot
	// needs: whether what is in the database came from the file on disk.
	state := indexer.NewStateStore(db)
	listLoaded := false
	if specs, err := repos.Load(cfg.ReposFile); errors.Is(err, fs.ErrNotExist) {
		slog.Warn("no repository list; not indexing, and answers come from whatever was indexed before",
			"path", cfg.ReposFile)
	} else if err != nil {
		slog.Error("repository list is invalid; refusing to start rather than run on a stale one",
			"path", cfg.ReposFile, "err", err)
		os.Exit(1)
	} else if purged, err := state.SyncSpecs(ctx, specs); err != nil {
		slog.Error("recording the repository list failed", "err", err)
		os.Exit(1)
	} else {
		for _, p := range purged {
			slog.Info("repository left the list; index purged", "repo", p.Name)
			// A snapshot's directory is an archive the operator extracted by
			// hand into the repository root. rongo did not create it and does
			// not delete it: the index is what governs answers, and destroying
			// somebody's unpacked source to tidy up after a YAML edit is not a
			// trade this makes. Re-adding the entry indexes it again from what
			// is still on disk.
			if p.Snapshot {
				slog.Info("the extracted snapshot directory was left in place",
					"repo", p.Name, "dir", filepath.Join(cfg.RepoRoot, p.Name))
				continue
			}
			// Not fatal. The index is already gone, which is what governs the
			// answers; a checkout left behind is disk, and exiting here would
			// turn a stale directory into a server that will not boot.
			if err := gitClient.RemoveCheckout(p.Name); err != nil {
				slog.Error("removing the purged checkout failed", "repo", p.Name, "err", err)
			}
		}
		listLoaded = true
	}

	// OUTSIDE the branch above, deliberately: every boot says what it holds,
	// including the one where the file was missing. That boot is exactly the one
	// where the question matters most — the database may carry a whole corpus
	// nobody just configured, and a silent start would leave the operator with
	// no way to tell an empty rongo from one serving a list that is no longer
	// on disk.
	//
	// Read back from the DATABASE, not from specs: the YAML says what was asked
	// for, this says what rongo actually holds — the resolved branch, the commit
	// it last indexed, how much of it, whether the last run failed, and the
	// declared uses edges.
	indexer.LogInventory(ctx, state, slog.Default(), cfg.ReposFile, listLoaded)

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
	switch {
	case cfg.IndexEnabled && !listLoaded:
		// No list on disk, but possibly a whole corpus in the database. The
		// poller reads state.Active() from that database, so starting it here
		// would fetch and re-index the PREVIOUS list, refresh every
		// last_run_at, and leave the Repos page reporting "Index current" for a
		// configuration that exists nowhere — which is the deception this
		// change exists to remove, reached by the other door. The index stays
		// and still answers; it simply stops moving, and a frozen last_run_at
		// is the honest signal.
		slog.Warn("not indexing: no repository list on disk",
			"path", cfg.ReposFile,
			"fix", "provide the file and restart; the existing index still answers until then")
	case cfg.IndexEnabled:
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
	default:
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
		Source:         sourceview.New(db, gitClient, cfg.IndexMaxFileBytes),
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
	// Timeout bounds one whole call, body included. The answer streams for as
	// long as its 16384-token budget takes, hidden reasoning counted, and the
	// default of five minutes would cut a slow one mid-answer: at 20 tokens a
	// second the budget needs close to 14 minutes. The idle watchdog, not this
	// one, is what catches a stalled upstream.
	models := llm.NewClient(llm.Config{
		BaseURL:       cfg.LLMBaseURL,
		APIKey:        cfg.LLMAPIKey,
		Timeout:       15 * time.Minute,
		IdleTimeout:   90 * time.Second,
		TurnMaxTokens: cfg.TurnMaxTokens,
	}, nil)
	// Said at boot like the inventory is: the ceiling is what stops a turn
	// nobody bounded, and a host running without one should be able to see
	// that in its log rather than find out from a bill.
	if cfg.TurnMaxTokens > 0 {
		slog.Info("turn token ceiling", "max_tokens", cfg.TurnMaxTokens)
	} else {
		slog.Warn("turn token ceiling off", "max_tokens", 0)
	}
	// One short-gate call reorders a pool of sixty before the cut to twenty:
	// measured twice on the pinned corpus (unique gathered 0.905 → 0.952,
	// composition 4/5 → 5/5, flow corpus 27/30 → 28/30), see
	// docs/measurements/2026-09-11-arms-after-the-crossing.md. It stores
	// nothing; a call that fails or a reply it cannot read keeps the fused
	// order, so the gate lane going down never fails a search.
	retriever := retrieve.New(db, embedder)
	retriever.Reranker = retrieve.NewLLMReranker(models, 60)
	deps.Ask = ask.NewPipeline(
		models,
		retriever,
		ask.NewGatherer(db, ask.GatherOptions{MaxHops: cfg.GatherMaxHops, TokenBudget: cfg.GatherTokenBudget}),
		ask.NewRouter(models, db, cfg.RouteMargin, moduleOpts(cfg)),
	)
	deps.Titler = func(ctx context.Context, question string, lang ask.Language) string {
		return ask.Title(ctx, models, question, lang)
	}
	deps.Suggester = func(ctx context.Context, question, answer string, audience ask.Audience,
		sources []ask.Source, scope ask.Scope, lang ask.Language,
	) []string {
		return ask.Followups(ctx, models, question, answer, audience, sources, scope, lang)
	}
	// Prices come from the registry: the MiMo deployments at MiMo's own API
	// listing whatever endpoint they are called at, the embedding model at
	// its endpoint. The table is read per report, so a fetch that lands after
	// boot prices the thread that is already open.
	deps.Prices = pricing.Start(pollCtx, &workers, pricing.Source{
		URL:          cfg.PricesURL,
		EmbedBaseURL: cfg.EmbedBaseURL,
		EmbedModel:   cfg.EmbedModel,
	})
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
