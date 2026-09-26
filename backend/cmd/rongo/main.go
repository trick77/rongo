// Command rongo serves the API and the embedded SPA.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
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
	"github.com/trick77/rongo/internal/history"
	"github.com/trick77/rongo/internal/httpapi"
	"github.com/trick77/rongo/internal/indexer"
	"github.com/trick77/rongo/internal/llm"
	"github.com/trick77/rongo/internal/memory"
	"github.com/trick77/rongo/internal/modules"
	"github.com/trick77/rongo/internal/release"
	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/repostatus"
	"github.com/trick77/rongo/internal/retrieve"
	"github.com/trick77/rongo/internal/sched"
	"github.com/trick77/rongo/internal/sourceview"
	"github.com/trick77/rongo/internal/store"
	"github.com/trick77/rongo/internal/symbols"
	"github.com/trick77/rongo/internal/threads"
	"golang.org/x/crypto/bcrypt"
)

// sweepExcluded applies BACKEND_INDEX_EXCLUDE and the built-in skip rules to
// every repository's existing index and refreshes the Repos page totals where
// it removed something.
func sweepExcluded(ctx context.Context, state *indexer.StateStore, pipeline *indexer.Indexer) {
	all, err := state.All(ctx)
	if err != nil {
		slog.Warn("exclusion sweep skipped; repository list unreadable", "err", err)
		return
	}
	for _, st := range all {
		changed, counts, err := pipeline.Sweep(ctx, st.Name)
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
		slog.Info("skipped files removed from the index", "repo", st.Name, "files", changed)
	}
}

// sweepSessions deletes expired sessions once a day until ctx ends. The boot
// already ran one; this is for a rongo that stays up for months.
func sweepSessions(ctx context.Context, authSvc *auth.Service) {
	for sched.Sleep(ctx, 24*time.Hour) {
		if n, err := authSvc.DeleteExpiredSessions(ctx, time.Now()); err != nil {
			slog.Warn("delete expired sessions", "err", err)
		} else if n > 0 {
			slog.Info("expired sessions removed", "sessions", n)
		}
	}
}

// hashPassword reads a password from stdin and prints its bcrypt hash, the
// value BACKEND_ADMIN_PASSWORD_HASH wants. Stdin, not an argument, so the
// plaintext never lands in a shell history or a process list.
func hashPassword(in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(in, 4096))
	if err != nil {
		return err
	}
	pw := strings.TrimRight(string(raw), "\r\n")
	if pw == "" {
		return errors.New("read an empty password from stdin")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(hash))
	return err
}

func main() {
	// Parsed before the config is read: hashing a password needs no
	// environment, and an operator setting rongo up has none yet.
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz and exit; used by the container healthcheck")
	hashPw := flag.Bool("hash-password", false, "read a password from stdin, print its bcrypt hash for BACKEND_ADMIN_PASSWORD_HASH, and exit")
	flag.Parse()
	if *hashPw {
		if err := hashPassword(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "hash-password: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

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

	if *healthcheck {
		// Bounded: a healthcheck that hangs is a container never reported
		// unhealthy.
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + cfg.Addr + "/healthz")
		if err != nil {
			os.Exit(1)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusOK {
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

	// Both model clients before the database is touched, so a missing
	// endpoint variable stops the boot where a config error would: with
	// nothing migrated, purged or swept.
	embedder, models, err := newModelClients(cfg, llm.Config{})
	if err != nil {
		slog.Error("model endpoint", "err", err)
		os.Exit(1)
	}

	// ctx is the process-wide root: startup work and the background workers all
	// hang off it, so a shutdown cancels everything from one place.
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o750); err != nil {
		slog.Error("create data directory", "err", err)
		os.Exit(1)
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()
	if err := migrateForModel(db); err != nil {
		slog.Error("prepare database", "err", err)
		os.Exit(1)
	}
	// A title call cannot outlive the process that started it, so a thread
	// still waiting for one was orphaned by the last shutdown. Left pending it
	// would hold "New question" in its header for good.
	threadStore := threads.NewStore(db)
	if err := threadStore.SettleTitles(ctx); err != nil {
		slog.Error("settle orphaned thread titles", "err", err)
		os.Exit(1)
	}
	// The same for the turns themselves: a row still unfinished now was left
	// by a crash, and a share's ceiling sits below the oldest unfinished row,
	// so an orphan would hold every later turn off the public page for good.
	if n, err := threadStore.FailOrphaned(ctx); err != nil {
		slog.Error("fail orphaned turns", "err", err)
		os.Exit(1)
	} else if n > 0 {
		slog.Warn("marked turns left unfinished by the last shutdown as failed", "turns", n)
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

	authSvc := auth.NewService(db, string(cfg.AuthMode), cfg.AdminToken)
	// Expired sessions are deleted at boot and once a day after; nothing
	// else ever removes them, and a table that only grows is a table that
	// one day does not fit.
	if n, err := authSvc.DeleteExpiredSessions(ctx, time.Now()); err != nil {
		slog.Error("delete expired sessions", "err", err)
		os.Exit(1)
	} else if n > 0 {
		slog.Info("expired sessions removed", "sessions", n)
	}
	if cfg.AuthMode == config.AuthModePassword {
		authSvc.SetPasswordAccount(cfg.AdminUser, cfg.AdminPasswordHash)
	}

	// Built before the list is synced: a repository that left the list is purged
	// from the database, and its checkout has to go with it.
	gitClient := gitrepo.New(tools.Git, cfg.RepoRoot).WithAuth(gitrepo.Auth{
		SSHKey: cfg.GitSSHKey, SSHKnownHosts: cfg.GitSSHKnownHosts, CAFile: cfg.GitCAFile,
	})

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
		// A purged repository's vectors go with its index.
		if len(purged) > 0 {
			indexer.PruneEmbedCacheAndLog(ctx, db, slog.Default(), "purged", len(purged))
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
		DB:       db,
		Git:      gitClient,
		Symbols:  symbols.NewExtractor(tools.Ctags),
		Embedder: embedder,
		Cache:    embed.NewCache(db, embed.Model, embed.Dim()),
		Writer:   indexer.NewWriter(db),
		Selector: indexer.NewSelector(indexer.SelectOptions{
			MaxBytes:       cfg.IndexMaxFileBytes,
			MaxDataBytes:   cfg.IndexMaxDataFileBytes,
			MaxSchemaBytes: cfg.IndexMaxSchemaFileBytes,
			Exclude:        cfg.IndexExclude,
		}),
		Chunk: chunkOptions(cfg),
	})

	// The commit lane: the branch's first-parent history beside the file
	// index, written by the poller and read by a "what changed" turn.
	commits := history.New(db)
	poller := indexer.NewPoller(indexer.PollerDeps{
		State:        state,
		Git:          gitClient,
		Index:        pipeline.IndexRepo,
		History:      commits,
		HistoryDepth: cfg.HistoryDepth,
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
	// Decided here, started below: the workers begin only after the last
	// step that can still exit the process (provider discovery, the listen),
	// so a fetch or an index transaction is never killed by a boot that
	// failed for an unrelated reason.
	indexing := false
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
		indexing = true
	default:
		slog.Warn("indexing is disabled; no repository will be fetched or embedded",
			"fix", "set BACKEND_INDEX_ENABLED=true")
	}

	// The viewer and the answer pipeline read files through the same service:
	// the viewer shows a citation, the pipeline reads a process model whose
	// nodes were cited, both at the indexed commit under the same rules.
	source := sourceview.New(db, gitClient, cfg.IndexMaxFileBytes).WithCommits(gitClient)
	deps := httpapi.Deps{
		Auth:           authSvc,
		Repos:          repostatus.New(db, moduleOpts(cfg)),
		Threads:        threads.NewStore(db),
		Source:         source,
		Commit:         source,
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
	retriever.Reranker = retrieve.NewLLMReranker(models, retrieve.DefaultRerankPool)
	// The locate loop, on by default at three rounds. One round measured the
	// same files as three when grep admitted chunks by address; now grep shows
	// every matching line and the model decides, which takes a look, a narrowing
	// and a confirmation. BACKEND_LOCATE_ROUNDS=0 switches it off.
	gatherer := ask.NewGatherer(db, ask.GatherOptions{MaxHops: cfg.GatherMaxHops, TokenBudget: cfg.GatherTokenBudget})
	if cfg.LocateRounds > 0 {
		gatherer = gatherer.WithLocateLoop(models, retriever).WithLocateRounds(cfg.LocateRounds)
		slog.Info("locate loop on", "rounds", cfg.LocateRounds)
	}
	deps.Ask = ask.NewPipeline(
		models,
		retriever,
		gatherer,
		ask.NewRouter(models, db, cfg.RouteMargin, moduleOpts(cfg)),
	).WithModels(source).WithHistory(commits, time.Now).
		// The release turn: tags and ancestry from the checkouts, the
		// commits between two deployed versions from the lane.
		WithReleases(release.New(gitClient, db, poller.Depth()))
	// The reader's standing instructions. Nil leaves the understanding
	// prompt as it was before memory existed, which is what the eval
	// baseline compares against.
	if cfg.Memory {
		deps.Memory = memory.NewStore(db)
	}
	deps.Titler = func(ctx context.Context, question string, lang ask.Language) string {
		return ask.Title(ctx, models, question, lang)
	}
	deps.Suggester = func(ctx context.Context, question, answer string, audience ask.Audience,
		sources []ask.Source, scope ask.Scope, lang ask.Language,
	) []string {
		return ask.Followups(ctx, models, question, answer, audience, sources, scope, lang)
	}
	// Only a poller that runs takes re-index requests: with indexing off, or
	// no list loaded, a request would be accepted, queued on the row and
	// served by nobody, and the page would show it queued for good.
	if indexing {
		deps.Reindex = poller
	}
	srv := httpapi.NewServer(deps)

	// Every handler's context hangs off this one, so a shutdown that runs
	// out of patience can cancel the answers still streaming instead of
	// abandoning them to os.Exit.
	handlerCtx, cancelHandlers := context.WithCancel(ctx)
	defer cancelHandlers()
	httpServer := &http.Server{
		Addr:        cfg.Addr,
		Handler:     srv,
		BaseContext: func(net.Listener) context.Context { return handlerCtx },
		// Reap connections that dawdle on headers or sit idle, e.g. a
		// misbehaving client or a scanner. Deliberately no WriteTimeout:
		// phase 4 streams SSE responses for minutes, and a global
		// WriteTimeout would cut those streams mid-response. Do not add one
		// here — per-handler deadlines, if ever needed, belong at the
		// handler level instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// The listen is the last thing that can refuse to boot, so it happens
	// before the workers start.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.Addr, "err", err)
		os.Exit(1)
	}
	if indexing {
		workers.Add(1)
		go func() {
			defer workers.Done()
			sweepExcluded(pollCtx, state, pipeline)
			poller.Run(pollCtx)
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		sweepSessions(pollCtx, authSvc)
	}()
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "auth_mode", string(cfg.AuthMode))
		serveErr <- httpServer.Serve(ln)
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
		cut, err := shutdown(httpServer, cancelHandlers, stopPolling, &workers, shutdownLimit)
		if cut {
			slog.Warn("answers still streaming were cut short", "limit", shutdownLimit.String())
		}
		if err != nil {
			slog.Error("graceful shutdown failed", "err", err)
			os.Exit(1)
		}
	}

	// Stop the background workers and wait for them, so a shutdown cannot leave
	// a git command or a half-written transaction behind. A no-op after
	// shutdown above; here for the server stopping on its own.
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

// newModelClients builds the embedder and the chat client. The endpoints are
// the env vars each model's llmwire profile names, read by llmwire; a missing
// one comes back named.
//
// One embedder for indexing and for the query side of every answer. It is
// needed whether or not indexing is on, so a missing variable is fatal either
// way. The chat client is always wired: a rongo that indexes but cannot
// answer is not a mode anyone wants to be in by accident. Its Timeout bounds
// one whole call, body included. The answer streams for as long as its
// budget takes, hidden reasoning counted, and a short default would cut a
// slow model mid-answer; config.DefaultLLMTimeout carries the arithmetic.
// The idle watchdog, not this one, is what catches a
// stalled upstream.
//
// chat carries what the environment does not: nothing in production, a
// synthetic registry and a fake's variables in a test.
func newModelClients(cfg config.Config, chat llm.Config) (*embed.Client, *llm.Client, error) {
	embedder, err := embed.NewClient(embed.Config{}, nil)
	if err != nil {
		return nil, nil, err
	}
	// The lanes are profile ids; llm.NewClient refuses one the registry does
	// not know or that cannot do its lane's work, naming the models that
	// could, before the database is touched. Logged here whether set or not:
	// which model answers is the first thing anyone reading a quality
	// complaint wants to know.
	chat.Timeout = cfg.LLMTimeout
	chat.TurnMaxTokens = cfg.TurnMaxTokens
	chat.Answer = cfg.LLMModel
	chat.Gate = cfg.LLMGateModel
	chat.GateTemperature = cfg.LLMGateTemperature
	chat.AnswerReasoning = cfg.LLMReasoning
	models, err := llm.NewClient(chat, nil)
	if err != nil {
		return nil, nil, err
	}
	gateTemp := "default"
	if cfg.LLMGateTemperature != nil {
		gateTemp = fmt.Sprint(*cfg.LLMGateTemperature)
	}
	answerReasoning := "default"
	if cfg.LLMReasoning != "" {
		answerReasoning = cfg.LLMReasoning
	}
	slog.Info("model lanes",
		"answer", models.Deployment(llm.LaneAnswer), "gate", models.Deployment(llm.LaneGate),
		"answer_reasoning", answerReasoning, "gate_temperature", gateTemp, "timeout", cfg.LLMTimeout)
	return embedder, models, nil
}

// migrateForModel applies the migrations at embed.Model's vector width and
// then refuses a database built at any other. The vec0 table's width is
// fixed when the file is created, so a build with a different embedding
// model pointed at an existing file fails here, loudly, rather than with a
// rejected insert on every chunk much later — and, worse, a semantic lane
// that silently answers nothing.
func migrateForModel(db *sql.DB) error {
	if err := store.Migrate(db, embed.Dim()); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	built, err := store.BuiltDim(db)
	if err != nil {
		return fmt.Errorf("read the vector table's dimension: %w", err)
	}
	if built != embed.Dim() {
		return fmt.Errorf("this database was built for a different embedding model: built %d wide, %s is %d wide; point BACKEND_DB_PATH at a fresh file, or run the build this database was made with",
			built, embed.Model, embed.Dim())
	}
	return nil
}
