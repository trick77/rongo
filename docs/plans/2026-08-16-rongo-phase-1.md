# rongo Phase 1 — Grundgerüst Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `rongo` binary that boots from env config, applies migrations, proves the sqlite-vec stack works, refuses to start with the wrong `ctags`, authenticates a user, and serves an embedded SPA — developable with one command and no Docker.

**Architecture:** One static Go binary (`CGO_ENABLED=0`) with the Vite/React SPA embedded via `//go:embed`, one SQLite file as the entire datastore. stdlib `net/http` with Go 1.22 method routing, no framework. Every collaborator is injected through a `Deps` struct so later phases can add the indexer, LLM client and SSE lanes without reshaping the server.

**Tech Stack:** Go (module `github.com/trick77/rongo`, `go` directive `1.25.0`, build image `golang:1.26-alpine`), `ncruces/go-sqlite3` v0.23.3, `asg017/sqlite-vec-go-bindings` v0.1.7-alpha.2, React 19 + TypeScript + Vite + Tailwind v4.

**Spec:** `docs/plans/rongo-spec.html` (also published as an Artifact). Repo conventions: `AGENTS.md`.

## Global Constraints

- `CGO_ENABLED=0` everywhere. Never introduce a cgo dependency.
- `ncruces/go-sqlite3` and `asg017/sqlite-vec-go-bindings` are **one unit**: pin both, bump both together or neither, never a lone Dependabot PR. Stay on `v0.23.3` / `v0.1.7-alpha.2` — the same pair as peeq and loom.
- One SQLite file is the whole datastore. No Postgres, no Redis, no vector service.
- stdlib `net/http` only. No web framework, no ORM, no router library.
- All runtime config comes from `RONGO_*` env vars. Secrets via env only, never committed.
- Structured `slog` only. The error attribute key is **`err`**, never `error`. Log `r.URL.Path`, **never** a full URL, `RequestURI()` or a query string.
- Docs, specs and code comments in **English**. UI copy and generated answers in **German, Swiss orthography** — never `ß`.
- No test hits a real LLM, embeddings endpoint or git remote.
- Feature branch `feat/phase-1-skeleton`. Never commit to `master`.
- Commit as `trick77@users.noreply.github.com`.

---

### Task 1: HTTP server skeleton with healthz and logging

**Files:**
- Create: `backend/go.mod`
- Create: `backend/cmd/rongo/main.go`
- Create: `backend/internal/httpapi/server.go`
- Create: `backend/internal/httpapi/middleware.go`
- Test: `backend/internal/httpapi/server_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `httpapi.NewServer(Deps) *Server` implementing `http.Handler`; `httpapi.Deps` struct (empty for now, grows each task).

- [ ] **Step 1: Initialise the module**

```bash
mkdir -p backend/cmd/rongo backend/internal/httpapi
cd backend && go mod init github.com/trick77/rongo && go mod edit -go=1.25.0
```

- [ ] **Step 2: Write the failing test**

`backend/internal/httpapi/server_test.go`:

```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz_returnsOK(t *testing.T) {
	// Given
	srv := NewServer(Deps{})

	// When
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestUnknownRoute_returns404(t *testing.T) {
	srv := NewServer(Deps{})

	req := httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run TestHealthz -v`
Expected: FAIL — `undefined: NewServer`

- [ ] **Step 4: Write the server**

`backend/internal/httpapi/server.go`:

```go
// Package httpapi wires rongo's HTTP routes onto the stdlib mux.
package httpapi

import "net/http"

// Deps holds every collaborator the HTTP layer needs. Each field is an
// interface declared here (consumer-side), so later phases can add the
// indexer, the LLM client and the retriever without touching call sites.
// A nil field means the feature is unconfigured; its endpoints answer 503.
type Deps struct{}

// Server routes requests and owns the middleware chain.
type Server struct {
	deps    Deps
	mux     *http.ServeMux
	handler http.Handler
}

// NewServer builds the router and wraps it in the middleware chain once,
// rather than per request.
func NewServer(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.routes()
	s.handler = recovery(logging(s.mux))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// routes is the single place every route is registered.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
```

`backend/internal/httpapi/middleware.go`:

```go
package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

// statusRecorder captures the status code so the access log can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logging emits one access line per request. It logs r.URL.Path and never the
// query string or full URL: query strings carry tokens and, later, OIDC codes.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		switch {
		case rec.status >= 500:
			slog.Error("request", attrs...)
		case rec.status >= 400:
			slog.Warn("request", attrs...)
		default:
			slog.Info("request", attrs...)
		}
	})
}

// recovery turns a panic into a 500 without killing the process.
func recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic recovered", "err", v, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
```

`backend/cmd/rongo/main.go`:

```go
// Command rongo serves the API and the embedded SPA.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/trick77/rongo/internal/httpapi"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	srv := httpapi.NewServer(httpapi.Deps{})

	addr := "127.0.0.1:8080"
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test ./internal/httpapi/ -v`
Expected: PASS — both tests.

- [ ] **Step 6: Verify it actually runs**

Run: `cd backend && go run ./cmd/rongo &` then `curl -s localhost:8080/healthz`
Expected: `{"status":"ok"}`. Kill the process afterwards.

- [ ] **Step 7: Commit**

```bash
git add backend/go.mod backend/cmd backend/internal/httpapi
git commit -m "feat: http server skeleton with healthz, logging and recovery"
```

---

### Task 2: Configuration from RONGO_* env vars

**Files:**
- Create: `backend/internal/config/config.go`
- Modify: `backend/cmd/rongo/main.go`
- Create: `.env.example`
- Test: `backend/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Load() (config.Config, error)`; `config.Config` with fields `Addr, PublicURL, DBPath, RepoRoot, AuthMode, AdminToken, SessionSecret, LogLevel`; `config.AuthMode` with constants `AuthModeDev`, `AuthModeToken`, `AuthModeOIDC`.

- [ ] **Step 1: Write the failing test**

`backend/internal/config/config_test.go`:

```go
package config

import "testing"

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoad_appliesDefaults(t *testing.T) {
	// Given
	setEnv(t, map[string]string{
		"RONGO_SESSION_SECRET": "s3cret",
	})

	// When
	cfg, err := Load()

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.Addr != "127.0.0.1:8080" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, "127.0.0.1:8080")
	}
	if cfg.DBPath != "./data/rongo.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "./data/rongo.db")
	}
	if cfg.RepoRoot != "./repos" {
		t.Errorf("RepoRoot = %q, want %q", cfg.RepoRoot, "./repos")
	}
	if cfg.AuthMode != AuthModeDev {
		t.Errorf("AuthMode = %q, want %q", cfg.AuthMode, AuthModeDev)
	}
}

func TestLoad_requiresSessionSecret(t *testing.T) {
	setEnv(t, map[string]string{"RONGO_SESSION_SECRET": ""})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want an error about RONGO_SESSION_SECRET")
	}
}

func TestLoad_devModeRefusesNonLoopbackAddr(t *testing.T) {
	// Given: dev mode auto-logs in an admin. Exposing that on 0.0.0.0 is an
	// open door, so the config layer refuses it rather than trusting operators.
	setEnv(t, map[string]string{
		"RONGO_SESSION_SECRET": "s3cret",
		"RONGO_AUTH_MODE":      "dev",
		"RONGO_ADDR":           "0.0.0.0:8080",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal to run dev auth on a non-loopback address")
	}
}

func TestLoad_tokenModeRequiresAdminToken(t *testing.T) {
	setEnv(t, map[string]string{
		"RONGO_SESSION_SECRET": "s3cret",
		"RONGO_AUTH_MODE":      "token",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want an error about RONGO_ADMIN_TOKEN")
	}
}

func TestLoad_rejectsUnknownAuthMode(t *testing.T) {
	setEnv(t, map[string]string{
		"RONGO_SESSION_SECRET": "s3cret",
		"RONGO_AUTH_MODE":      "kerberos",
	})

	_, err := Load()

	if err == nil {
		t.Fatal("Load() err = nil, want an error about an unknown auth mode")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/config/ -v`
Expected: FAIL — `undefined: Load`

- [ ] **Step 3: Write the config package**

`backend/internal/config/config.go`:

```go
// Package config loads rongo's runtime configuration from environment
// variables. Every setting is RONGO_*; secrets come from the environment only.
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
		Addr:          envOr("RONGO_ADDR", "127.0.0.1:8080"),
		PublicURL:     envOr("RONGO_PUBLIC_URL", "http://127.0.0.1:8080"),
		DBPath:        envOr("RONGO_DB_PATH", "./data/rongo.db"),
		RepoRoot:      envOr("RONGO_REPO_ROOT", "./repos"),
		AuthMode:      AuthMode(envOr("RONGO_AUTH_MODE", string(AuthModeDev))),
		AdminToken:    os.Getenv("RONGO_ADMIN_TOKEN"),
		SessionSecret: os.Getenv("RONGO_SESSION_SECRET"),
		LogLevel:      envOr("RONGO_LOG_LEVEL", "info"),
	}

	if cfg.SessionSecret == "" {
		return Config{}, fmt.Errorf("RONGO_SESSION_SECRET is required")
	}

	switch cfg.AuthMode {
	case AuthModeDev:
		if !isLoopback(cfg.Addr) {
			return Config{}, fmt.Errorf(
				"RONGO_AUTH_MODE=dev signs in an admin without credentials and is only allowed on a loopback address, got RONGO_ADDR=%q", cfg.Addr)
		}
	case AuthModeToken:
		if cfg.AdminToken == "" {
			return Config{}, fmt.Errorf("RONGO_AUTH_MODE=token requires RONGO_ADMIN_TOKEN")
		}
	case AuthModeOIDC:
		// Wired in a later phase; the mode is accepted so deployments can be
		// prepared, and the auth layer answers 501 until it exists.
	default:
		return Config{}, fmt.Errorf("unknown RONGO_AUTH_MODE %q (want dev, token or oidc)", cfg.AuthMode)
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/config/ -v`
Expected: PASS — all five tests.

- [ ] **Step 5: Wire config into main**

Replace the body of `backend/cmd/rongo/main.go`:

```go
// Command rongo serves the API and the embedded SPA.
package main

import (
	"log/slog"
	"net/http"
	"os"

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
```

Add `"fmt"` and `"strings"` to the import block.

- [ ] **Step 6: Write `.env.example`**

```bash
cat > .env.example <<'EOF'
# rongo runtime configuration. Copy to .env and fill in.
# Every setting is RONGO_*. Secrets live here only, never in repos.yaml.

# --- HTTP ---
RONGO_ADDR=127.0.0.1:8080
RONGO_PUBLIC_URL=http://127.0.0.1:8080
RONGO_LOG_LEVEL=info

# --- Storage ---
RONGO_DB_PATH=./data/rongo.db
RONGO_REPO_ROOT=./repos

# --- Auth ---
# dev  : auto-login as admin, loopback addresses only
# token: every request needs RONGO_ADMIN_TOKEN as a bearer token
# oidc : production (implemented in a later phase)
RONGO_AUTH_MODE=dev
RONGO_ADMIN_TOKEN=
RONGO_SESSION_SECRET=change-me
EOF
```

- [ ] **Step 7: Verify the guard rails by hand**

Run: `cd backend && RONGO_SESSION_SECRET=x RONGO_AUTH_MODE=dev RONGO_ADDR=0.0.0.0:8080 go run ./cmd/rongo`
Expected: exits 1 with the loopback refusal on stderr.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/config backend/cmd/rongo/main.go .env.example
git commit -m "feat: load and validate runtime config from RONGO_* env vars"
```

---

### Task 3: SQLite store and embedded migration runner

**Files:**
- Create: `backend/internal/store/store.go`
- Create: `backend/internal/store/migrate.go`
- Create: `backend/internal/store/migrations/0001_init.sql`
- Test: `backend/internal/store/migrate_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `store.Open(path string) (*sql.DB, error)`; `store.Migrate(db *sql.DB) error`; `store.VecLiteral(v []float32) string`.

- [ ] **Step 1: Add the pinned dependencies**

```bash
cd backend
go get github.com/ncruces/go-sqlite3@v0.23.3
go get github.com/asg017/sqlite-vec-go-bindings@v0.1.7-alpha.2
go mod tidy
```

Verify the versions match peeq and loom exactly:

```bash
grep -E "ncruces/go-sqlite3|sqlite-vec-go-bindings" go.mod
```
Expected: `v0.23.3` and `v0.1.7-alpha.2`.

- [ ] **Step 2: Write the failing test**

`backend/internal/store/migrate_test.go`:

```go
package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openTemp gives each test its own database file.
func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("Open() err = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrate_createsSchemaAndIsIdempotent(t *testing.T) {
	// Given
	db := openTemp(t)

	// When
	if err := Migrate(db); err != nil {
		t.Fatalf("first Migrate() err = %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate() err = %v", err)
	}

	// Then: the tables exist ...
	for _, table := range []string{"users", "sessions", "schema_migrations"} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}

	// ... and the second run recorded nothing extra.
	var applied int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 1 {
		t.Errorf("schema_migrations rows = %d, want 1", applied)
	}
}

func TestOpen_enablesWALAndForeignKeys(t *testing.T) {
	db := openTemp(t)

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd backend && go test ./internal/store/ -v`
Expected: FAIL — `undefined: Open`

- [ ] **Step 4: Write the store**

`backend/internal/store/store.go`:

```go
// Package store opens the single SQLite database (pure-Go ncruces driver with
// the sqlite-vec WASM build linked in) and applies the embedded migrations.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	// The sqlite-vec WASM build for ncruces. It provides the SQLite WASM
	// binary AND the vec0 virtual table plus vec_* functions, and replaces
	// ncruces/go-sqlite3/embed. These two modules are one unit: never bump one
	// without the other. See AGENTS.md.
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	// registers the "sqlite3" database/sql driver.
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Open opens (creating if needed) the database at path with the pragmas rongo
// relies on. Callers run Migrate separately.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(on)",
		url.PathEscape(path),
	)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

// VecLiteral encodes a float32 vector as the JSON-array text sqlite-vec
// accepts as a bind parameter.
func VecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
```

`backend/internal/store/migrate.go`:

```go
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, each in its own transaction, in lexicographic filename
// order.
//
// The recorded version is the FULL FILENAME. Never edit a migration that has
// run anywhere real: the runner skips a recorded version, so the edit silently
// never applies. Write the next numbered file instead.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var dummy int
		err := db.QueryRow(`SELECT 1 FROM schema_migrations WHERE version = ?`, name).Scan(&dummy)
		if err == nil {
			continue // already applied
		}
		if err != sql.ErrNoRows {
			return err
		}

		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
```

`backend/internal/store/migrations/0001_init.sql`:

```sql
-- users: one row per identity rongo has seen. subject is the stable id from
-- the auth mode in use (the fixed dev user, or the OIDC subject later).
CREATE TABLE users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    subject    TEXT NOT NULL UNIQUE,
    email      TEXT NOT NULL DEFAULT '',
    is_admin   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- sessions: server-side opaque tokens. Only the SHA-256 of the token is
-- stored, so a database copy cannot be replayed as a login.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);

CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test ./internal/store/ -v`
Expected: PASS — both tests.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/store backend/go.mod backend/go.sum
git commit -m "feat: sqlite store with embedded migration runner"
```

---

### Task 4: vec0 and FTS5 smoke test

This task adds no production code. Its entire purpose is to make an
incompatible bump of the sqlite/sqlite-vec pair fail CI instead of surfacing
in production as silently empty result sets. Vector search is not built until
phase 2 — the guard is needed before that, not after.

**Files:**
- Test: `backend/internal/store/vec_smoke_test.go`

**Interfaces:**
- Consumes: `store.Open`, `store.VecLiteral` from Task 3.
- Produces: nothing.

- [ ] **Step 1: Write the test**

`backend/internal/store/vec_smoke_test.go`:

```go
package store

import "testing"

// TestVec0_returnsNearestNeighbour proves the sqlite-vec WASM build is loaded
// and functional in the ncruces driver. It is a version-skew alarm: the two
// modules are compiled against each other, so a mismatched bump breaks vector
// search at runtime rather than at compile time.
func TestVec0_returnsNearestNeighbour(t *testing.T) {
	// Given: three orthogonal unit vectors.
	db := openTemp(t)
	if _, err := db.Exec(`CREATE VIRTUAL TABLE vec_smoke USING vec0(embedding float[3])`); err != nil {
		t.Fatalf("create vec0 table: %v (is the sqlite-vec build loaded?)", err)
	}
	vectors := []struct {
		rowid int
		vec   []float32
	}{
		{1, []float32{1, 0, 0}},
		{2, []float32{0, 1, 0}},
		{3, []float32{0, 0, 1}},
	}
	for _, v := range vectors {
		if _, err := db.Exec(
			`INSERT INTO vec_smoke(rowid, embedding) VALUES (?, ?)`,
			v.rowid, VecLiteral(v.vec),
		); err != nil {
			t.Fatalf("insert rowid %d: %v", v.rowid, err)
		}
	}

	// When: querying close to the first vector. vec0 requires the `k = ?`
	// constraint to sit alongside the MATCH.
	var rowid int
	var distance float64
	err := db.QueryRow(
		`SELECT rowid, distance FROM vec_smoke
		 WHERE embedding MATCH ? AND k = ?
		 ORDER BY distance`,
		VecLiteral([]float32{0.9, 0.1, 0}), 1,
	).Scan(&rowid, &distance)

	// Then
	if err != nil {
		t.Fatalf("knn query: %v", err)
	}
	if rowid != 1 {
		t.Errorf("nearest rowid = %d, want 1", rowid)
	}
	if distance <= 0 || distance > 1 {
		t.Errorf("distance = %f, want a small positive L2 distance", distance)
	}
}

// TestFTS5_isAvailable proves FTS5 is compiled into the same WASM build. The
// hybrid retriever in phase 2 depends on both lanes living in one driver.
func TestFTS5_isAvailable(t *testing.T) {
	// Given
	db := openTemp(t)
	if _, err := db.Exec(`CREATE VIRTUAL TABLE fts_smoke USING fts5(text)`); err != nil {
		t.Fatalf("create fts5 table: %v (is FTS5 compiled in?)", err)
	}
	if _, err := db.Exec(
		`INSERT INTO fts_smoke(rowid, text) VALUES (1, 'teaser mail versand'), (2, 'gutschein ablauf')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// When
	var rowid int
	err := db.QueryRow(`SELECT rowid FROM fts_smoke WHERE text MATCH 'teaser'`).Scan(&rowid)

	// Then
	if err != nil {
		t.Fatalf("match query: %v", err)
	}
	if rowid != 1 {
		t.Errorf("rowid = %d, want 1", rowid)
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `cd backend && go test ./internal/store/ -run 'TestVec0|TestFTS5' -v`
Expected: PASS — both. If `CREATE VIRTUAL TABLE ... USING vec0` fails, the sqlite/sqlite-vec pin is broken; do not work around it, fix the versions.

- [ ] **Step 3: Prove it runs without cgo**

Run: `cd backend && CGO_ENABLED=0 go test ./internal/store/ -count=1`
Expected: PASS. This is the claim the whole storage choice rests on.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/store/vec_smoke_test.go
git commit -m "test: vec0 and fts5 smoke tests guarding the sqlite-vec pin"
```

---

### Task 5: External tool resolution with a universal-ctags check

**Files:**
- Create: `backend/internal/exttools/exttools.go`
- Modify: `backend/cmd/rongo/main.go`
- Test: `backend/internal/exttools/exttools_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `exttools.Resolve() (exttools.Paths, error)`; `exttools.Paths{Git, Rg, Ctags string}`.

- [ ] **Step 1: Write the failing test**

`backend/internal/exttools/exttools_test.go`:

```go
package exttools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin writes an executable shell script that prints body for --version.
func fakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	script := "#!/bin/sh\ncat <<'OUT'\n" + body + "\nOUT\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// onlyPath points PATH at dir so the test controls which binaries exist.
func onlyPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

func TestResolve_acceptsUniversalCtags(t *testing.T) {
	// Given
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "rg", "ripgrep 15.2.0")
	fakeBin(t, dir, "ctags", "Universal Ctags 6.1.0, Copyright (C) 2015-2024")
	onlyPath(t, dir)

	// When
	paths, err := Resolve()

	// Then
	if err != nil {
		t.Fatalf("Resolve() err = %v, want nil", err)
	}
	if paths.Ctags != filepath.Join(dir, "ctags") {
		t.Errorf("Ctags = %q, want %q", paths.Ctags, filepath.Join(dir, "ctags"))
	}
}

func TestResolve_rejectsBSDCtags(t *testing.T) {
	// Given: macOS ships this at /usr/bin/ctags. Accepting it would produce an
	// empty symbol index rather than an error, which is far worse.
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "rg", "ripgrep 15.2.0")
	fakeBin(t, dir, "ctags", "usage: ctags [-BFTaduwvx] [-f tagsfile] file ...")
	onlyPath(t, dir)

	// When
	_, err := Resolve()

	// Then
	if err == nil {
		t.Fatal("Resolve() err = nil, want a rejection of BSD ctags")
	}
	if !strings.Contains(err.Error(), "universal-ctags") {
		t.Errorf("error = %q, want it to name universal-ctags so the fix is obvious", err)
	}
}

func TestResolve_reportsMissingBinary(t *testing.T) {
	// Given: git and ctags present, rg absent.
	dir := t.TempDir()
	fakeBin(t, dir, "git", "git version 2.48.0")
	fakeBin(t, dir, "ctags", "Universal Ctags 6.1.0")
	onlyPath(t, dir)

	// When
	_, err := Resolve()

	// Then
	if err == nil {
		t.Fatal("Resolve() err = nil, want an error naming ripgrep")
	}
	if !strings.Contains(err.Error(), "rg") {
		t.Errorf("error = %q, want it to name rg", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/exttools/ -v`
Expected: FAIL — `undefined: Resolve`

- [ ] **Step 3: Write the package**

`backend/internal/exttools/exttools.go`:

```go
// Package exttools locates the external binaries rongo shells out to and
// verifies they are the ones it actually needs.
//
// The dev environment runs without a container, so the binaries come from the
// developer's machine and cannot be assumed correct. ctags in particular: macOS
// ships Apple's BSD ctags at /usr/bin/ctags, which rejects long options. Using
// it would yield an empty symbol index instead of an error, so rongo refuses to
// start rather than indexing silently wrong.
package exttools

import (
	"fmt"
	"os/exec"
	"strings"
)

// Paths holds the resolved absolute paths of the external tools.
type Paths struct {
	Git   string
	Rg    string
	Ctags string
}

// Resolve finds every required binary and validates ctags. It returns the
// first problem it finds, phrased so the fix is obvious from the message.
func Resolve() (Paths, error) {
	var p Paths
	var err error

	if p.Git, err = exec.LookPath("git"); err != nil {
		return Paths{}, fmt.Errorf("git not found in PATH: %w", err)
	}
	if p.Rg, err = exec.LookPath("rg"); err != nil {
		return Paths{}, fmt.Errorf("ripgrep (rg) not found in PATH: %w", err)
	}
	if p.Ctags, err = exec.LookPath("ctags"); err != nil {
		return Paths{}, fmt.Errorf("ctags not found in PATH (install universal-ctags): %w", err)
	}
	if err := verifyUniversalCtags(p.Ctags); err != nil {
		return Paths{}, err
	}
	return p, nil
}

// verifyUniversalCtags checks the banner rather than trusting the filename.
func verifyUniversalCtags(path string) error {
	out, err := exec.Command(path, "--version").CombinedOutput()
	banner := firstLine(string(out))
	if err != nil {
		return fmt.Errorf(
			"%s --version failed (%q); macOS ships BSD ctags at /usr/bin/ctags — install universal-ctags (brew install universal-ctags)",
			path, banner)
	}
	if !strings.Contains(string(out), "Universal Ctags") {
		return fmt.Errorf(
			"%s is not universal-ctags (reports %q); install universal-ctags (brew install universal-ctags) and make sure it precedes /usr/bin on PATH",
			path, banner)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/exttools/ -v`
Expected: PASS — all three tests.

- [ ] **Step 5: Call it at startup**

In `backend/cmd/rongo/main.go`, after the logger is configured and before the server starts:

```go
	tools, err := exttools.Resolve()
	if err != nil {
		slog.Error("required external tool missing or wrong", "err", err)
		os.Exit(1)
	}
	slog.Info("external tools resolved", "git", tools.Git, "rg", tools.Rg, "ctags", tools.Ctags)
```

Add `"github.com/trick77/rongo/internal/exttools"` to the imports.

- [ ] **Step 6: Verify on this machine**

Run: `cd backend && RONGO_SESSION_SECRET=x go run ./cmd/rongo`
Expected: on a machine without universal-ctags, exit 1 naming universal-ctags and the brew command. After `brew install universal-ctags`, the server starts and logs the three paths.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/exttools backend/cmd/rongo/main.go
git commit -m "feat: resolve external tools and reject BSD ctags at startup"
```

---

### Task 6: Sessions, dev auto-login and admin bearer token

**Files:**
- Create: `backend/internal/auth/session.go`
- Create: `backend/internal/auth/service.go`
- Create: `backend/internal/auth/middleware.go`
- Modify: `backend/internal/httpapi/server.go`
- Modify: `backend/cmd/rongo/main.go`
- Test: `backend/internal/auth/session_test.go`
- Test: `backend/internal/auth/middleware_test.go`

**Interfaces:**
- Consumes: `store.Open`, `store.Migrate`.
- Produces: `auth.NewService(db *sql.DB, mode config.AuthMode, adminToken string) *auth.Service`; `(*Service).Middleware(next http.Handler) http.Handler`; `auth.UserFrom(ctx context.Context) (auth.User, bool)`; `auth.User{ID int64, Subject, Email string, IsAdmin bool}`; cookie name `rongo_session`.

- [ ] **Step 1: Write the failing session test**

`backend/internal/auth/session_test.go`:

```go
package auth

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewService(db, "dev", "")
}

func TestCreateSession_returnsTokenResolvableToUser(t *testing.T) {
	// Given
	svc := newService(t)
	user, err := svc.UpsertUser("dev-user", "dev@example.invalid", true)
	if err != nil {
		t.Fatalf("UpsertUser() err = %v", err)
	}

	// When
	token, err := svc.CreateSession(user.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession() err = %v", err)
	}
	got, ok := svc.UserByToken(token)

	// Then
	if !ok {
		t.Fatal("UserByToken() ok = false, want true")
	}
	if got.ID != user.ID {
		t.Errorf("user id = %d, want %d", got.ID, user.ID)
	}
}

func TestCreateSession_storesOnlyTheHash(t *testing.T) {
	// Given: a database copy must not be replayable as a login.
	svc := newService(t)
	user, _ := svc.UpsertUser("dev-user", "dev@example.invalid", true)

	// When
	token, err := svc.CreateSession(user.ID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession() err = %v", err)
	}

	// Then
	var count int
	if err := svc.db.QueryRow(
		`SELECT count(*) FROM sessions WHERE token_hash = ?`, token,
	).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Error("the raw token is stored in sessions.token_hash; store its SHA-256 instead")
	}
}

func TestUserByToken_rejectsExpiredSession(t *testing.T) {
	// Given
	svc := newService(t)
	user, _ := svc.UpsertUser("dev-user", "dev@example.invalid", true)
	token, _ := svc.CreateSession(user.ID, -time.Minute) // already expired

	// When
	_, ok := svc.UserByToken(token)

	// Then
	if ok {
		t.Error("UserByToken() ok = true for an expired session, want false")
	}
}

func TestUserByToken_rejectsUnknownToken(t *testing.T) {
	svc := newService(t)

	_, ok := svc.UserByToken("not-a-real-token")

	if ok {
		t.Error("UserByToken() ok = true for an unknown token, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/auth/ -v`
Expected: FAIL — `undefined: NewService`

- [ ] **Step 3: Write the session layer**

`backend/internal/auth/session.go`:

```go
// Package auth identifies callers. Phase 1 ships the dev and token modes; the
// OIDC seam exists so a later phase adds a mode, not a redesign.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// SessionCookie is the cookie carrying the opaque session token.
const SessionCookie = "rongo_session"

// User is an authenticated identity.
type User struct {
	ID      int64
	Subject string
	Email   string
	IsAdmin bool
}

// Service owns users and sessions.
type Service struct {
	db         *sql.DB
	mode       string
	adminToken string
}

// NewService builds the auth service. adminToken is only consulted in token
// mode.
func NewService(db *sql.DB, mode string, adminToken string) *Service {
	return &Service{db: db, mode: mode, adminToken: adminToken}
}

// UpsertUser inserts the subject or returns the existing row.
func (s *Service) UpsertUser(subject, email string, isAdmin bool) (User, error) {
	admin := 0
	if isAdmin {
		admin = 1
	}
	if _, err := s.db.Exec(
		`INSERT INTO users (subject, email, is_admin) VALUES (?, ?, ?)
		 ON CONFLICT(subject) DO UPDATE SET email = excluded.email, is_admin = excluded.is_admin`,
		subject, email, admin,
	); err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	var u User
	var adminInt int
	if err := s.db.QueryRow(
		`SELECT id, subject, email, is_admin FROM users WHERE subject = ?`, subject,
	).Scan(&u.ID, &u.Subject, &u.Email, &adminInt); err != nil {
		return User{}, fmt.Errorf("read user: %w", err)
	}
	u.IsAdmin = adminInt == 1
	return u, nil
}

// CreateSession mints a random token, stores only its SHA-256, and returns the
// raw token to hand to the client exactly once.
func (s *Service) CreateSession(userID int64, ttl time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := s.db.Exec(
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), userID, time.Now().Add(ttl).UTC().Format(time.RFC3339),
	); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}
	return token, nil
}

// UserByToken resolves a raw token to its user, rejecting expired sessions.
func (s *Service) UserByToken(token string) (User, bool) {
	var u User
	var adminInt int
	var expiresAt string
	err := s.db.QueryRow(
		`SELECT u.id, u.subject, u.email, u.is_admin, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ?`,
		hashToken(token),
	).Scan(&u.ID, &u.Subject, &u.Email, &adminInt, &expiresAt)
	if err != nil {
		return User{}, false
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || time.Now().After(exp) {
		return User{}, false
	}
	u.IsAdmin = adminInt == 1
	return u, true
}

// DeleteSession revokes one session.
func (s *Service) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run the session tests**

Run: `cd backend && go test ./internal/auth/ -run TestCreateSession -v`
Expected: PASS.

- [ ] **Step 5: Write the failing middleware test**

`backend/internal/auth/middleware_test.go`:

```go
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// protected reports whether the wrapped handler was reached.
func protected(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_devModeAutoLogsInAsAdmin(t *testing.T) {
	// Given
	svc := newService(t)
	var got User
	var reached bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The user is attached to the request passed downstream, so it must be
		// read here rather than from the original request.
		got, reached = mustUser(t, r)
		w.WriteHeader(http.StatusOK)
	})

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	svc.Middleware(handler).ServeHTTP(rec, req)

	// Then
	if !reached {
		t.Fatal("handler not reached; dev mode should sign the caller in")
	}
	if got.Subject != devSubject {
		t.Errorf("subject = %q, want %q", got.Subject, devSubject)
	}
	if !got.IsAdmin {
		t.Error("dev user is not admin, want admin")
	}
}

// mustUser reads the authenticated user off the request context.
func mustUser(t *testing.T, r *http.Request) (User, bool) {
	t.Helper()
	u, ok := UserFrom(r.Context())
	return u, ok
}

func TestMiddleware_tokenModeRejectsMissingToken(t *testing.T) {
	// Given
	svc := newService(t)
	svc.mode = "token"
	svc.adminToken = "s3cret-token"
	var reached bool

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	// Then
	if reached {
		t.Error("handler reached without a token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestMiddleware_tokenModeAcceptsBearerToken(t *testing.T) {
	// Given
	svc := newService(t)
	svc.mode = "token"
	svc.adminToken = "s3cret-token"
	var reached bool

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer s3cret-token")
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	// Then
	if !reached {
		t.Fatalf("handler not reached; status = %d", rec.Code)
	}
}

func TestMiddleware_tokenModeRejectsWrongToken(t *testing.T) {
	svc := newService(t)
	svc.mode = "token"
	svc.adminToken = "s3cret-token"
	var reached bool

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	if reached {
		t.Error("handler reached with the wrong token")
	}
}

func TestMiddleware_acceptsSessionCookie(t *testing.T) {
	// Given
	svc := newService(t)
	svc.mode = "token"
	svc.adminToken = "s3cret-token"
	user, _ := svc.UpsertUser("someone", "someone@example.invalid", false)
	token, _ := svc.CreateSession(user.ID, time.Hour)
	var reached bool

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	svc.Middleware(protected(&reached)).ServeHTTP(rec, req)

	// Then
	if !reached {
		t.Fatalf("handler not reached; status = %d", rec.Code)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd backend && go test ./internal/auth/ -run TestMiddleware -v`
Expected: FAIL — `svc.Middleware undefined`

- [ ] **Step 7: Write the middleware**

`backend/internal/auth/middleware.go`:

```go
package auth

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type contextKey struct{}

var userKey contextKey

// devSubject is the fixed identity dev mode signs in. config refuses dev mode
// on a non-loopback address, so this never reaches a network.
const devSubject = "dev-user"

// UserFrom returns the authenticated user attached by Middleware.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey).(User)
	return u, ok
}

// Middleware authenticates a request in whichever mode is configured and
// attaches the user to the request context. It fails closed: any path that
// does not positively identify a caller answers 401.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A valid session cookie wins in every mode.
		if c, err := r.Cookie(SessionCookie); err == nil {
			if u, ok := s.UserByToken(c.Value); ok {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
				return
			}
		}

		switch s.mode {
		case "dev":
			u, err := s.UpsertUser(devSubject, "dev@example.invalid", true)
			if err != nil {
				slog.Error("dev auto-login failed", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return

		case "token":
			presented, ok := bearerToken(r)
			// Constant-time compare: a length-independent equality check on a
			// shared secret leaks it a byte at a time.
			if !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(s.adminToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			u, err := s.UpsertUser("admin-token", "", true)
			if err != nil {
				slog.Error("admin token login failed", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return

		case "oidc":
			// The seam. Until the OIDC flow lands, an unauthenticated caller
			// gets 401 rather than a misleading 500.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return

		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	})
}

// SetSessionCookie writes the session cookie. Secure is set whenever the
// public URL is https.
func SetSessionCookie(w http.ResponseWriter, token, publicURL string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(publicURL, "https://"),
		Expires:  time.Now().Add(ttl),
	})
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return h[len(prefix):], true
}
```

- [ ] **Step 8: Run the auth tests**

Run: `cd backend && go test ./internal/auth/ -v`
Expected: PASS — all tests.

- [ ] **Step 9: Add the /api/me endpoint**

In `backend/internal/httpapi/server.go`, extend `Deps` and `routes`:

```go
// Deps holds every collaborator the HTTP layer needs.
type Deps struct {
	Auth *auth.Service
}
```

```go
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.Handle("GET /api/me", s.requireAuth(http.HandlerFunc(s.handleMe)))
}

// requireAuth is the single gate every authenticated route goes through.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.deps.Auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "auth unavailable", http.StatusServiceUnavailable)
		})
	}
	return s.deps.Auth.Middleware(next)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"subject":  u.Subject,
		"email":    u.Email,
		"is_admin": u.IsAdmin,
	})
}
```

Add `"encoding/json"` and `"github.com/trick77/rongo/internal/auth"` to the imports.

- [ ] **Step 10: Wire the store and auth service into main**

In `backend/cmd/rongo/main.go`, between the tool check and the server:

```go
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
```

Add the `auth` and `store` imports. Note `cfg.DBPath` defaults to `./data/rongo.db`; create the directory first:

```go
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		slog.Error("create data directory", "err", err)
		os.Exit(1)
	}
```

Add `"path/filepath"` to the imports.

- [ ] **Step 11: Verify by hand**

Run: `cd backend && RONGO_SESSION_SECRET=x go run ./cmd/rongo` then `curl -s localhost:8080/api/me`
Expected: `{"subject":"dev-user","email":"dev@example.invalid","is_admin":true}`

Run with token mode: `RONGO_SESSION_SECRET=x RONGO_AUTH_MODE=token RONGO_ADMIN_TOKEN=abc go run ./cmd/rongo`, then `curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/api/me`
Expected: `401`. With `-H 'Authorization: Bearer abc'`: `200`.

- [ ] **Step 12: Commit**

```bash
git add backend/internal/auth backend/internal/httpapi backend/cmd/rongo/main.go
git commit -m "feat: sessions, dev auto-login and admin bearer token"
```

---

### Task 7: Embedded SPA shell

**Files:**
- Create: `ui/package.json`, `ui/vite.config.ts`, `ui/tsconfig.json`, `ui/index.html`
- Create: `ui/src/main.tsx`, `ui/src/App.tsx`, `ui/src/index.css`
- Create: `backend/web/embed.go`
- Create: `backend/web/dist/index.html` (tracked placeholder)
- Modify: `backend/internal/httpapi/server.go`
- Test: `backend/internal/httpapi/spa_test.go`

**Interfaces:**
- Consumes: `httpapi.Deps` from Task 6.
- Produces: `web.Handler() http.Handler` serving the embedded SPA with an index.html fallback.

- [ ] **Step 1: Scaffold the UI**

```bash
mkdir -p ui/src backend/web/dist
cd ui
npm init -y
npm install react react-dom
npm install -D vite @vitejs/plugin-react typescript @types/react @types/react-dom tailwindcss @tailwindcss/vite vitest jsdom @testing-library/react
```

`ui/vite.config.ts`:

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // The SPA is built straight into the Go binary's embed directory.
  build: { outDir: "../backend/web/dist", emptyOutDir: true },
  server: {
    host: "127.0.0.1",
    proxy: { "/api": "http://127.0.0.1:8080" },
  },
});
```

`ui/index.html`:

```html
<!doctype html>
<html lang="de">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>rongo</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

`ui/src/index.css` — the design tokens from the mock (`docs/plans/rongo-ui-mock.html`):

```css
@import "tailwindcss";

@theme {
  --color-ground: #f1f2ef;
  --color-surface: #ffffff;
  --color-sunk: #e8eae6;
  --color-ink: #161b1c;
  --color-ink-soft: #5c6668;
  --color-ink-faint: #8b9496;
  --color-hairline: #dbded8;
  --color-accent: #106257;
  --color-ochre: #8a6412;
}

body {
  background: var(--color-ground);
  color: var(--color-ink);
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
}
```

`ui/src/main.tsx`:

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import "./index.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
```

`ui/src/App.tsx` — the shell only; the real screens arrive in phase 4:

```tsx
export default function App() {
  return (
    <main className="mx-auto max-w-2xl p-8">
      <h1 className="text-2xl font-semibold tracking-tight">rongo</h1>
      <p className="mt-2 text-[var(--color-ink-soft)]">
        Noch nichts indexiert. Die Oberfläche entsteht in einer späteren Phase.
      </p>
    </main>
  );
}
```

- [ ] **Step 2: Write the tracked placeholder**

`backend/web/dist/index.html` — this file is tracked so `go:embed` always has something to embed; built assets are gitignored.

```html
<!doctype html>
<html lang="de">
  <head>
    <meta charset="utf-8" />
    <title>rongo</title>
  </head>
  <body>
    <p>SPA not built. Run <code>make fe-build</code>.</p>
  </body>
</html>
```

- [ ] **Step 3: Write the failing test**

`backend/internal/httpapi/spa_test.go`:

```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPA_servesIndexAtRoot(t *testing.T) {
	// Given
	srv := NewServer(Deps{})

	// When
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Errorf("body does not look like the SPA shell: %q", rec.Body.String())
	}
}

func TestSPA_fallsBackForClientRoutes(t *testing.T) {
	// Given: the SPA owns its own routing, so an unknown non-API path must
	// return index.html rather than 404.
	srv := NewServer(Deps{})

	req := httptest.NewRequest(http.MethodGet, "/threads/42", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestSPA_doesNotSwallowAPIRoutes(t *testing.T) {
	// Given: an unknown /api path must stay a 404, never the SPA shell.
	srv := NewServer(Deps{})

	req := httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run TestSPA -v`
Expected: FAIL — root returns 404.

- [ ] **Step 5: Write the embed package**

`backend/web/embed.go`:

```go
// Package web serves the embedded single-page application.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the built SPA. Any path that is not a real file falls back to
// index.html so the client-side router can take over; /api paths are excluded
// so a typo in an endpoint stays a 404 instead of silently returning HTML.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist directory missing from embed: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 6: Mount it**

In `backend/internal/httpapi/server.go`, add to `routes()` as the last registration:

```go
	// "/" is the catch-all: everything not matched above goes to the SPA.
	s.mux.Handle("/", web.Handler())
```

Add `"github.com/trick77/rongo/web"` to the imports.

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd backend && go test ./internal/httpapi/ -v`
Expected: PASS — all tests including the three SPA ones.

- [ ] **Step 8: Build the real SPA and check it in the browser**

```bash
cd ui && npm run build
cd ../backend && RONGO_SESSION_SECRET=x go run ./cmd/rongo
```
Open `http://127.0.0.1:8080/` and confirm the shell renders. Then restore the placeholder, which the build overwrote:

```bash
git checkout -- backend/web/dist/index.html
```

- [ ] **Step 9: Commit**

```bash
git add ui backend/web backend/internal/httpapi
git commit -m "feat: embed the SPA shell with an index.html fallback"
```

---

### Task 8: One-command dev loop without Docker

**Files:**
- Create: `Makefile`
- Create: `hack/dev.sh`
- Create: `docs/manual-verification.md`

**Interfaces:**
- Consumes: everything above.
- Produces: `make dev`, `make test`, `make fe-test`, `make fe-build`, `make build`, `make run`, `make tidy`.

- [ ] **Step 1: Write the Makefile**

```makefile
.PHONY: build test fe-build fe-test run dev tidy

tidy:
	cd backend && go mod tidy

test:
	cd backend && go test ./...

fe-test:
	cd ui && npm run test -- --run

fe-build:
	cd ui && npm ci && npm run build

build: fe-build
	cd backend && CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/rongo ./cmd/rongo

run:
	cd backend && go run ./cmd/rongo

dev:
	./hack/dev.sh
```

- [ ] **Step 2: Write the dev script**

`hack/dev.sh` — the dev environment runs without Docker, so this starts both processes natively and Vite proxies `/api` to the backend.

```sh
#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DB_PATH=${RONGO_DB_PATH:-/tmp/rongo-dev.db}
REPO_ROOT=${RONGO_REPO_ROOT:-/tmp/rongo-dev-repos}

cleanup() {
  if [ -n "${BACKEND_PID:-}" ]; then
    kill "$BACKEND_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

mkdir -p "$REPO_ROOT"

(
  cd "$ROOT/backend"
  RONGO_SESSION_SECRET=${RONGO_SESSION_SECRET:-dev-secret} \
  RONGO_AUTH_MODE=dev \
  RONGO_ADDR=127.0.0.1:8080 \
  RONGO_PUBLIC_URL=http://127.0.0.1:8080 \
  RONGO_DB_PATH="$DB_PATH" \
  RONGO_REPO_ROOT="$REPO_ROOT" \
  go run ./cmd/rongo
) &
BACKEND_PID=$!

cd "$ROOT/ui"
npm run dev -- --host 127.0.0.1
```

```bash
chmod +x hack/dev.sh
```

- [ ] **Step 3: Write the manual verification doc**

`docs/manual-verification.md`:

```markdown
# Manual verification

Flows that no automated test covers, because they need real binaries or a real
browser. Run these by hand before calling a phase done.

## Phase 1 — skeleton

Prerequisites (the dev environment runs without Docker, so these come from your
machine):

- `git`
- `rg` (ripgrep)
- `ctags` — **universal-ctags**, not the BSD ctags macOS ships at
  `/usr/bin/ctags`. Install with `brew install universal-ctags` and make sure it
  precedes `/usr/bin` on `PATH`.

Checks:

1. `make dev` starts the backend and Vite; `http://127.0.0.1:5173/` shows the
   shell and `/api/me` returns the dev user through the proxy.
2. Temporarily put a directory containing only BSD `ctags` first on `PATH` and
   run `make run`. It must exit 1 naming universal-ctags — not start with an
   empty symbol index.
3. `RONGO_AUTH_MODE=dev RONGO_ADDR=0.0.0.0:8080 make run` must refuse to start.
4. `make build` produces `bin/rongo`; running it serves the built SPA at
   `http://127.0.0.1:8080/`.
```

- [ ] **Step 4: Verify the dev loop end to end**

Run: `make dev`
Expected: backend logs `listening`, Vite serves on 5173, `curl -s localhost:5173/api/me` returns the dev user through the proxy. Ctrl-C stops both.

- [ ] **Step 5: Commit**

```bash
git add Makefile hack/dev.sh docs/manual-verification.md
git commit -m "feat: one-command dev loop without Docker"
```

---

### Task 9: Container image and compose file

**Files:**
- Create: `backend/Containerfile`
- Create: `compose.yaml`

**Interfaces:**
- Consumes: the Makefile build target.
- Produces: a runnable image.

- [ ] **Step 1: Write the Containerfile**

`backend/Containerfile` — three stages. The runtime is `debian:13-slim`, not distroless, because rongo shells out to `git`, `rg` and `ctags`.

```dockerfile
# --- Stage 1: build the SPA -------------------------------------------------
FROM node:26-alpine AS ui
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

# --- Stage 2: build the binary ---------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# The SPA lands in the embed directory before the Go build reads it.
COPY --from=ui /backend/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/rongo ./cmd/rongo

# --- Stage 3: runtime -------------------------------------------------------
# Deliberately NOT distroless: rongo shells out to git, ripgrep and
# universal-ctags, all of which need a real userland.
FROM debian:13-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates git ripgrep universal-ctags \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/rongo /usr/local/bin/rongo
USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/rongo"]
```

Note the stage-1 output path: Vite writes to `../backend/web/dist`, so inside the `ui` stage the artefacts land at `/backend/web/dist`. Adjust the `WORKDIR` if that path does not match after the first build — verify with `docker build --target ui`.

- [ ] **Step 2: Write compose.yaml**

`compose.yaml` — one service, matching peeq's shape.

```yaml
services:
  rongo:
    build:
      context: .
      dockerfile: backend/Containerfile
    image: ghcr.io/trick77/rongo:latest
    restart: unless-stopped
    user: "1000:1000"
    cap_drop: [ALL]
    read_only: true
    tmpfs:
      - /tmp
    environment:
      RONGO_ADDR: 0.0.0.0:8080
      RONGO_PUBLIC_URL: ${RONGO_PUBLIC_URL}
      RONGO_DB_PATH: /data/rongo.db
      RONGO_REPO_ROOT: /repos
      RONGO_AUTH_MODE: ${RONGO_AUTH_MODE}
      RONGO_ADMIN_TOKEN: ${RONGO_ADMIN_TOKEN}
      RONGO_SESSION_SECRET: ${RONGO_SESSION_SECRET}
      RONGO_LOG_LEVEL: ${RONGO_LOG_LEVEL:-info}
    volumes:
      - ./data:/data
      - ./repos:/repos
    healthcheck:
      test: ["CMD", "/usr/local/bin/rongo", "-healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
```

The healthcheck needs a `-healthcheck` flag in `main.go` that requests `/healthz` on the configured address and exits 0 or 1. Add it:

```go
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz and exit; used by the container healthcheck")
	flag.Parse()
	if *healthcheck {
		resp, err := http.Get("http://" + cfg.Addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}
```

Place it directly after `config.Load()` and add `"flag"` to the imports.

- [ ] **Step 3: Build the image**

Run: `docker build -f backend/Containerfile -t rongo:dev .`
Expected: build succeeds. Then verify the tools are present in the runtime image:

```bash
docker run --rm --entrypoint sh rongo:dev -c 'git --version && rg --version | head -1 && ctags --version | head -1'
```
Expected: the ctags line contains `Universal Ctags`. If it does not, the runtime image would fail the startup check — fix the apt package before moving on.

- [ ] **Step 4: Commit**

```bash
git add backend/Containerfile compose.yaml backend/cmd/rongo/main.go
git commit -m "feat: container image and compose file"
```

---

### Task 10: CI workflow — HELD BACK

**You explicitly deferred pipeline scripts when the repository was created. Do
not execute this task until it is released.** Everything above works without
it; the tests run locally through `make test` and `make fe-test`.

When released, mirror `peeq/.github/workflows/ci.yaml`: a `backend` job running
`go build`, `go vet`, a gofmt gate and `go test -race` (with `CGO_ENABLED=1`
**only** for the race detector, never for the build), and a `ui` job running
format check, build and vitest. Add `.github/dependabot.yaml` with the sqlite
pair **grouped into one PR** — a lone bump of either module is exactly the
failure the vec0 smoke test exists to catch, and grouping stops it reaching
review as two separate changes.

---

## Definition of done for phase 1

- [ ] `make test` and `make fe-test` pass.
- [ ] `CGO_ENABLED=0 go test ./...` passes — the storage choice rests on this.
- [ ] `make dev` starts backend and Vite together, no Docker involved.
- [ ] `make build` produces `bin/rongo`; the binary serves the SPA and `/api/me`.
- [ ] `docker build` succeeds and the runtime image reports `Universal Ctags`.
- [ ] `docs/manual-verification.md` phase-1 checks all pass by hand.
- [ ] Branch `feat/phase-1-skeleton` is pushed and a PR is open against `master`.
