# rongo Phase 2 — Indexierung Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** rongo clones the repositories listed in `repos.yaml`, keeps them current by diff, extracts symbols, chunks the source, embeds it, and answers a hybrid vector+keyword query over the result — measured against a fixed question set at the end.

**Architecture:** A poller goroutine drives a DB-table queue (peeq's `sched` pattern). Indexing is a pipeline: select files → extract symbols with `ctags` → chunk → enrich → embed through a content-hash cache → write to `chunks` + `chunks_vec` + `chunks_fts` in one transaction. Retrieval fuses a `vec0` KNN lane and an FTS5 keyword lane with weighted Reciprocal Rank Fusion, ported from `peeq/backend/internal/rag`.

**Tech Stack:** Go (module `github.com/trick77/rongo`), `ncruces/go-sqlite3` v0.23.3 + `asg017/sqlite-vec-go-bindings` v0.1.7-alpha.2, an OpenAI-compatible `/embeddings` endpoint, and `git` / `rg` / `universal-ctags` as external binaries.

**Spec:** `docs/plans/rongo-spec.html`. Repo conventions: `AGENTS.md`. Phase 1 plan, for the interfaces it produced: `docs/plans/2026-08-16-rongo-phase-1.md`.

## Global Constraints

- `CGO_ENABLED=0` everywhere. Never introduce a cgo dependency.
- `ncruces/go-sqlite3` and `asg017/sqlite-vec-go-bindings` are **one unit**: bump both together or neither, never a lone Dependabot PR. Stay on `v0.23.3` / `v0.1.7-alpha.2`.
- One SQLite file is the whole datastore. No Postgres, no Redis, no vector service.
- stdlib `net/http` only. No web framework, no ORM, no router library.
- All runtime config comes from `BACKEND_*` env vars. **Repository credentials come only from the environment, never from `repos.yaml`.**
- **No tree-sitter.** `ctags` gives a uniform record across ~150 languages; where it yields nothing, the line window is the normal path, not a failure path.
- Structured `slog` only; error attribute key **`err`**, never `error`. Never log a token, a full URL, or a query string.
- Docs, specs, code comments, UI copy and generated answers in **English**.
- No test hits a real LLM, embeddings endpoint or git remote — `httptest` fakes, and a fixture repository built locally with `git`.
- Feature branch `feat/phase-2-indexing`. Never commit to `master`. Commit as `trick77@users.noreply.github.com`.

## Invariants this phase must not break

- **Never invent.** A file rongo could not read, or a repository it has not indexed, is recorded as such — never silently omitted.
- **No hit means no hit.** An empty result set is reported with the terms that were tried.
- **Every claim is citable**: repo, branch, file, line. Every chunk carries all four.
- A repo dropping out of `repos.yaml` is **deactivated, not deleted**.
- A configured branch vanishing upstream is a **loud error**, never a silent stop.

## What this phase does NOT include

Feature cards (phase 3), the ask pipeline and SSE (phase 4), cost tracking (phase 5), history documents (phase 6). The retrieval built here is the machinery those phases call; it has no HTTP surface yet beyond a debug endpoint.

---

### Task 1: repos.yaml — schema, loading and validation

**Files:**
- Create: `backend/internal/repos/config.go`
- Create: `repos.example.yaml`
- Modify: `backend/internal/config/config.go`, `.env.example`
- Test: `backend/internal/repos/config_test.go`

**Interfaces:**
- Consumes: `config.Config` from phase 1.
- Produces: `repos.Load(path string) ([]repos.Spec, error)`; `repos.Spec{Name, CloneURL, Branch, TokenEnv string, Enabled bool}`; `config.Config.ReposFile` from `BACKEND_REPOS_FILE` (default `./repos.yaml`).

- [ ] **Step 1: Write the failing test**

`backend/internal/repos/config_test.go`:

```go
package repos

import (
	"os"
	"path/filepath"
	"testing"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}
	return path
}

func TestLoad_readsEntries(t *testing.T) {
	// Given
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: https://forge.example.invalid/acme/shop-backend.git
    branch: master
    token_env: BACKEND_FORGE_TOKEN
  - name: commons-mail
    clone_url: https://forge.example.invalid/acme/commons-mail.git
`)

	// When
	specs, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if len(specs) != 2 {
		t.Fatalf("len(specs) = %d, want 2", len(specs))
	}
	if specs[0].Branch != "master" {
		t.Errorf("specs[0].Branch = %q, want %q", specs[0].Branch, "master")
	}
	// An omitted branch stays EMPTY here. Resolving it needs the remote, which
	// this package must not touch; the git layer resolves it later.
	if specs[1].Branch != "" {
		t.Errorf("specs[1].Branch = %q, want empty (resolved from the remote later)", specs[1].Branch)
	}
	if !specs[1].Enabled {
		t.Error("specs[1].Enabled = false, want true by default")
	}
}

func TestLoad_rejectsInlineSecret(t *testing.T) {
	// Given: token_env exists precisely so the secret is NOT in this file, which
	// ends up in a repository or a ticket sooner or later.
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: https://user:ghp_realtokenvalue@forge.example.invalid/acme/shop.git
`)

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("Load() err = nil, want a refusal of credentials embedded in clone_url")
	}
}

func TestLoad_rejectsDuplicateNames(t *testing.T) {
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: https://example.invalid/a.git
  - name: shop-backend
    clone_url: https://example.invalid/b.git
`)

	_, err := Load(path)

	if err == nil {
		t.Fatal("Load() err = nil, want a duplicate-name error")
	}
}

func TestLoad_rejectsUnsafeName(t *testing.T) {
	// Given: the name becomes a directory under BACKEND_REPO_ROOT.
	path := writeYAML(t, `
repositories:
  - name: ../escape
    clone_url: https://example.invalid/a.git
`)

	_, err := Load(path)

	if err == nil {
		t.Fatal("Load() err = nil, want a rejection of a name that escapes the repo root")
	}
}

func TestLoad_missingFileIsAnError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))

	if err == nil {
		t.Fatal("Load() err = nil, want an error naming the missing file")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/repos/ -v`
Expected: FAIL — `undefined: Load`

- [ ] **Step 3: Add the YAML dependency**

The repo has no third-party dependency beyond the SQLite pair. This adds one deliberately: hand-rolling a YAML parser is the worse option.

```
cd backend && go get gopkg.in/yaml.v3 && go mod tidy
```

- [ ] **Step 4: Write the loader**

`backend/internal/repos/config.go`:

```go
// Package repos loads and validates the repository list. The list lives in a
// YAML file rather than a database table so it is versionable and reviewable in
// a diff; credentials deliberately do NOT live there, because that file ends up
// in a repository or a ticket sooner or later.
package repos

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is one repository rongo indexes.
type Spec struct {
	// Name identifies the repository and becomes its directory under
	// BACKEND_REPO_ROOT, so it must be a safe single path segment.
	Name string
	// CloneURL must not embed credentials; see TokenEnv.
	CloneURL string
	// Branch is optional. Empty means "resolve the remote's default branch",
	// which is NOT necessarily master — this corpus mixes master and main.
	Branch string
	// TokenEnv names the environment variable holding the access token for this
	// repository's forge. The value never appears in the YAML.
	TokenEnv string
	// Enabled defaults to true; set it false to stop indexing without deleting.
	Enabled bool
}

type file struct {
	Repositories []rawSpec `yaml:"repositories"`
}

// rawSpec exists so Enabled can default to true. A plain bool would default to
// false and silently disable every entry that omits the field.
type rawSpec struct {
	Name     string `yaml:"name"`
	CloneURL string `yaml:"clone_url"`
	Branch   string `yaml:"branch"`
	TokenEnv string `yaml:"token_env"`
	Enabled  *bool  `yaml:"enabled"`
}

// Load reads and validates the repository list, returning the first problem it
// finds rather than indexing a half-valid list.
func Load(path string) ([]Spec, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read repository list %s: %w", path, err)
	}
	var f file
	if err := yaml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := make(map[string]bool, len(f.Repositories))
	specs := make([]Spec, 0, len(f.Repositories))
	for i, r := range f.Repositories {
		if err := validateName(r.Name); err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate repository name %q", r.Name)
		}
		seen[r.Name] = true

		if err := validateCloneURL(r.Name, r.CloneURL); err != nil {
			return nil, err
		}

		enabled := true
		if r.Enabled != nil {
			enabled = *r.Enabled
		}
		specs = append(specs, Spec{
			Name:     r.Name,
			CloneURL: strings.TrimSpace(r.CloneURL),
			Branch:   strings.TrimSpace(r.Branch),
			TokenEnv: strings.TrimSpace(r.TokenEnv),
			Enabled:  enabled,
		})
	}
	return specs, nil
}

// validateName keeps the name usable as a single directory segment under the
// repository root. A separator or a parent reference would let the YAML write
// outside BACKEND_REPO_ROOT.
func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || name == "." {
		return fmt.Errorf(
			"name %q must be a single path segment without separators or parent references", name)
	}
	return nil
}

// validateCloneURL refuses credentials embedded in the URL.
func validateCloneURL(name, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%s: clone_url is required", name)
	}
	u, err := url.Parse(raw)
	if err != nil {
		// An scp-style git URL (git@host:org/repo.git) does not parse as a URL
		// and carries no password field, so it passes.
		if strings.Contains(raw, "@") && strings.Contains(raw, ":") {
			return nil
		}
		return fmt.Errorf("%s: clone_url is not a valid URL: %w", name, err)
	}
	if u.User != nil {
		return fmt.Errorf(
			"%s: clone_url must not embed credentials — put the token in an env var and name it with token_env",
			name)
	}
	return nil
}
```

- [ ] **Step 5: Write `repos.example.yaml`**

```yaml
# The repositories rongo indexes. Copy to repos.yaml.
#
# Credentials NEVER belong in this file — it ends up in a repository or a ticket
# sooner or later. Name the environment variable with token_env instead, and put
# the value in the environment.
#
# branch is optional. Omitting it means "the remote's default branch", which is
# not necessarily master: this corpus mixes master and main.
repositories:
  - name: peeq
    clone_url: /Users/jan/localgit/peeq
    # branch omitted on purpose: resolved from the remote

  - name: loom
    clone_url: /Users/jan/localgit/loom

  # A dependency peeq genuinely declares, so cross-repo composition is exercised
  # against real data rather than a fixture.
  - name: go-sqlite3
    clone_url: https://github.com/ncruces/go-sqlite3.git

  # Deliberately NOT listed, so the "the boundary is named" invariant is
  # exercised in daily use rather than only in tests:
  #   asg017/sqlite-vec
```

- [ ] **Step 6: Add the config field**

In `backend/internal/config/config.go`, add to `Config`:

```go
	// ReposFile is the path to the repository list. Its entries carry no
	// secrets; tokens are named by token_env and read from the environment.
	ReposFile string
```

and in `Load`:

```go
		ReposFile: envOr("BACKEND_REPOS_FILE", "./repos.yaml"),
```

Document it in `.env.example` under a new `# --- Indexing ---` heading.

- [ ] **Step 7: Run tests**

Run: `cd backend && go test ./internal/repos/ ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```
git add backend/internal/repos backend/internal/config repos.example.yaml .env.example backend/go.mod backend/go.sum
git commit -m "feat: load and validate the repository list from repos.yaml"
```

---

### Task 2: Git operations — clone, fetch, branch resolution, diff

**Files:**
- Create: `backend/internal/gitrepo/gitrepo.go`
- Test: `backend/internal/gitrepo/gitrepo_test.go`

**Interfaces:**
- Consumes: `exttools.Paths.Git` (phase 1), `repos.Spec` (task 1).
- Produces: `gitrepo.New(gitBin, root string) *Client`; `(*Client).Dir(spec) string`; `EnsureCloned(ctx, spec, token) error`; `DefaultBranch(ctx, spec) (string, error)`; `Fetch(ctx, spec, token) error`; `HeadSHA(ctx, spec, branch) (string, error)`; `ChangedPaths(ctx, spec, fromSHA, toSHA) ([]string, error)`; `ListPaths(ctx, spec, sha) ([]string, error)`; `ReadFile(ctx, spec, sha, path) ([]byte, error)`; `gitrepo.ErrBranchGone`.

- [ ] **Step 1: Write the failing test**

`backend/internal/gitrepo/gitrepo_test.go`:

```go
package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/repos"
)

// gitRun runs git in dir with a deterministic identity.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fixtureRepo builds a real local repository whose default branch is main.
// The "remote" is a directory on disk, so no test ever touches the network.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	writeAndCommit(t, dir, "a.txt", "first\n", "first")
	return dir
}

func writeAndCommit(t *testing.T, dir, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-qm", msg)
}

func newClient(t *testing.T) *Client {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	return New(gitBin, t.TempDir())
}

func TestDefaultBranch_resolvesFromRemoteNotAssumed(t *testing.T) {
	// Given: a remote whose default branch is main, not master.
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Enabled: true}

	// When
	branch, err := c.DefaultBranch(context.Background(), spec)

	// Then
	if err != nil {
		t.Fatalf("DefaultBranch() err = %v", err)
	}
	if branch != "main" {
		t.Errorf("DefaultBranch() = %q, want %q — never assume master", branch, "main")
	}
}

func TestChangedPaths_reportsOnlyTheDiff(t *testing.T) {
	// Given
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	before, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}

	// When: one new file upstream.
	writeAndCommit(t, src, "b.txt", "second\n", "second")
	if err := c.Fetch(ctx, spec, ""); err != nil {
		t.Fatalf("Fetch() err = %v", err)
	}
	after, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}
	changed, err := c.ChangedPaths(ctx, spec, before, after)

	// Then
	if err != nil {
		t.Fatalf("ChangedPaths() err = %v", err)
	}
	if before == after {
		t.Fatal("HeadSHA did not move after a new upstream commit")
	}
	if len(changed) != 1 || changed[0] != "b.txt" {
		t.Errorf("ChangedPaths() = %v, want exactly [b.txt] — a needless full re-index is the bug this prevents", changed)
	}
}

func TestHeadSHA_reportsErrBranchGone(t *testing.T) {
	// Given: a configured branch that does not exist upstream. This must be an
	// identifiable error — a silent stop freezes the index while every status
	// looks healthy, and answers then come from months-old code.
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "release-2024.3", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}

	// When
	_, err := c.HeadSHA(ctx, spec, "release-2024.3")

	// Then
	if !errors.Is(err, ErrBranchGone) {
		t.Fatalf("HeadSHA() err = %v, want ErrBranchGone", err)
	}
}

func TestReadFile_readsFromTheIndexedCommit(t *testing.T) {
	// Given
	src := fixtureRepo(t)
	c := newClient(t)
	spec := repos.Spec{Name: "fixture", CloneURL: src, Branch: "main", Enabled: true}
	ctx := context.Background()
	if err := c.EnsureCloned(ctx, spec, ""); err != nil {
		t.Fatalf("EnsureCloned() err = %v", err)
	}
	sha, err := c.HeadSHA(ctx, spec, "main")
	if err != nil {
		t.Fatalf("HeadSHA() err = %v", err)
	}

	// When
	body, err := c.ReadFile(ctx, spec, sha, "a.txt")

	// Then
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	if string(body) != "first\n" {
		t.Errorf("ReadFile() = %q, want %q", body, "first\n")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/gitrepo/ -v`
Expected: FAIL — `undefined: New`

- [ ] **Step 3: Write the client**

`backend/internal/gitrepo/gitrepo.go`:

```go
// Package gitrepo drives the git binary. rongo clones and owns its checkouts
// rather than reading through a forge API: an API cannot grep, per-file fetches
// rate-limit at this corpus size, and the "why is it like this" pipeline needs
// local history.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/trick77/rongo/internal/repos"
)

// ErrBranchGone reports that a configured branch does not exist on the remote.
// It is a named error because the caller must surface it loudly: a silent stop
// freezes the index while every status looks healthy.
var ErrBranchGone = errors.New("configured branch not found")

// Client runs git commands against checkouts under root.
type Client struct {
	git  string
	root string
}

// New builds a Client. gitBin comes from exttools.Resolve.
func New(gitBin, root string) *Client {
	return &Client{git: gitBin, root: root}
}

// Dir is where a repository's checkout lives.
func (c *Client) Dir(spec repos.Spec) string {
	return filepath.Join(c.root, spec.Name)
}

// EnsureCloned clones the repository if it is not present. The clone keeps full
// history because the "why" pipeline reads it, and source is cheap on disk.
func (c *Client) EnsureCloned(ctx context.Context, spec repos.Spec, token string) error {
	dir := c.Dir(spec)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return fmt.Errorf("create repository root: %w", err)
	}
	_, err := c.run(ctx, c.root, "clone", "--quiet", authURL(spec.CloneURL, token), dir)
	return err
}

// DefaultBranch asks the remote which branch it considers default. Never assume
// master: this corpus mixes master and main.
func (c *Client) DefaultBranch(ctx context.Context, spec repos.Spec) (string, error) {
	out, err := c.run(ctx, c.root, "ls-remote", "--symref", spec.CloneURL, "HEAD")
	if err != nil {
		return "", err
	}
	// The symref line reads: "ref: refs/heads/main\tHEAD"
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "ref:") {
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 2 {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}
	return "", fmt.Errorf("%s: remote reported no default branch", spec.Name)
}

// Fetch updates the remote-tracking refs.
func (c *Client) Fetch(ctx context.Context, spec repos.Spec, token string) error {
	_, err := c.run(ctx, c.Dir(spec), "fetch", "--quiet", "--prune", authURL(spec.CloneURL, token))
	return err
}

// HeadSHA returns the commit the branch points at on the remote-tracking side.
// A missing branch yields ErrBranchGone.
func (c *Client) HeadSHA(ctx context.Context, spec repos.Spec, branch string) (string, error) {
	out, err := c.run(ctx, c.Dir(spec), "rev-parse", "--verify", "--quiet",
		"refs/remotes/origin/"+branch)
	if err != nil || strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%s: branch %q: %w", spec.Name, branch, ErrBranchGone)
	}
	return strings.TrimSpace(out), nil
}

// ChangedPaths lists the paths differing between two commits. This is what
// keeps a push from costing a full re-index. It is valid across branches too,
// because both commits live in the same object store.
func (c *Client) ChangedPaths(ctx context.Context, spec repos.Spec, fromSHA, toSHA string) ([]string, error) {
	out, err := c.run(ctx, c.Dir(spec), "diff", "--name-only", fromSHA+".."+toSHA)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// ListPaths lists every tracked path at a commit, for the initial index.
func (c *Client) ListPaths(ctx context.Context, spec repos.Spec, sha string) ([]string, error) {
	out, err := c.run(ctx, c.Dir(spec), "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// ReadFile reads one path at one commit. Reading from the commit rather than
// the working tree keeps every chunk attributable to an exact SHA.
func (c *Client) ReadFile(ctx context.Context, spec repos.Spec, sha, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.git, "show", sha+":"+path)
	cmd.Dir = c.Dir(spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read %s at %s: %w: %s", path, shortSHA(sha), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (c *Client) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.git, args...)
	cmd.Dir = dir
	// Never let git prompt: a hung credential prompt would stall the poller
	// forever with no output to diagnose it.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// redact keeps a token out of an error that will be logged.
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err, redact(stderr.String()))
	}
	return stdout.String(), nil
}

// authURL injects the token into an https remote for the duration of one
// command. It is never written to disk and never logged.
func authURL(raw, token string) string {
	if token == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return raw
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

// redact removes anything resembling embedded credentials from text about to be
// logged.
func redact(s string) string {
	var b strings.Builder
	for i, field := range strings.Fields(s) {
		if i > 0 {
			b.WriteByte(' ')
		}
		if u, err := url.Parse(field); err == nil && u.User != nil {
			u.User = url.User("REDACTED")
			b.WriteString(u.String())
			continue
		}
		b.WriteString(field)
	}
	return b.String()
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
```

- [ ] **Step 4: Run tests**

Run: `cd backend && go test ./internal/gitrepo/ -v`
Expected: PASS — all four tests.

- [ ] **Step 5: Prove no test reaches the network**

Run: `cd backend && go test ./internal/gitrepo/ -count=1 2>&1 | grep -ci 'github.com' || true`
Expected: `0`. The fixture "remote" is a local directory; a test that reached a real host would hang rather than fail fast.

- [ ] **Step 6: Commit**

```
git add backend/internal/gitrepo
git commit -m "feat: git client for clone, fetch, branch resolution and diff"
```
---

### Task 3: Repository state, migration and the poller

**Files:**
- Create: `backend/internal/store/migrations/0002_index.sql`
- Create: `backend/internal/indexer/state.go`
- Create: `backend/internal/indexer/poller.go`
- Create: `backend/internal/sched/sched.go`
- Modify: `backend/cmd/rongo/main.go`
- Test: `backend/internal/indexer/state_test.go`, `backend/internal/indexer/poller_test.go`

**Interfaces:**
- Consumes: `repos.Load`, `gitrepo.Client`, `store.Open/Migrate`.
- Produces: `indexer.NewStateStore(db) *StateStore` with `SyncSpecs(ctx, []repos.Spec) error`, `Active(ctx) ([]RepoState, error)`, `MarkIndexed(ctx, name, sha string, counts Counts) error`, `MarkError(ctx, name, msg string) error`; `indexer.RepoState{Name, Branch, CloneURL, LastSHA, LastError string, Enabled bool, LastRunAt time.Time}`; `sched.Jittered(d time.Duration) time.Duration`; `sched.Sleep(ctx, d) bool`.

- [ ] **Step 1: Write the migration**

`backend/internal/store/migrations/0002_index.sql`:

```sql
-- repo_state: one row per repository rongo knows about. Rows survive a repo
-- leaving repos.yaml (enabled = 0) rather than being deleted: a typo in the
-- YAML must not destroy hours of indexing. Only an explicit admin purge removes
-- the index.
CREATE TABLE repo_state (
    name        TEXT PRIMARY KEY,
    clone_url   TEXT NOT NULL,
    branch      TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_sha    TEXT NOT NULL DEFAULT '',
    last_run_at TEXT NOT NULL DEFAULT '',
    -- last_error is surfaced on the Repos page. A configured branch that
    -- vanished upstream lands here; it must never be a silent stop, because a
    -- frozen index looks healthy while answers come from months-old code.
    last_error  TEXT NOT NULL DEFAULT '',
    file_count  INTEGER NOT NULL DEFAULT 0,
    chunk_count INTEGER NOT NULL DEFAULT 0
);

-- files: one row per indexed path at the commit it was indexed from, so every
-- chunk is attributable to an exact repo/branch/file/sha.
CREATE TABLE files (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    repo      TEXT NOT NULL REFERENCES repo_state(name) ON DELETE CASCADE,
    path      TEXT NOT NULL,
    sha       TEXT NOT NULL,
    lang      TEXT NOT NULL DEFAULT '',
    size      INTEGER NOT NULL DEFAULT 0,
    UNIQUE (repo, path)
);

CREATE INDEX idx_files_repo ON files(repo);

-- symbols: from ctags. Not embedded — this is the exact-name lookup table and
-- the basis of the cross-repo reference walk.
CREATE TABLE symbols (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    kind    TEXT NOT NULL DEFAULT '',
    line    INTEGER NOT NULL DEFAULT 0,
    scope   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_symbols_name ON symbols(name);
CREATE INDEX idx_symbols_file ON symbols(file_id);

-- chunks: the unit of retrieval. text is the ENRICHED text that was embedded
-- (breadcrumb + enclosing symbols + doc comment + body); raw_text is what the
-- keyword lane indexes and what a citation quotes.
CREATE TABLE chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    ordinal     INTEGER NOT NULL,
    start_line  INTEGER NOT NULL,
    end_line    INTEGER NOT NULL,
    symbol      TEXT NOT NULL DEFAULT '',
    text        TEXT NOT NULL,
    raw_text    TEXT NOT NULL,
    token_count INTEGER NOT NULL DEFAULT 0,
    -- content_hash keys the embedding cache. Moved or renamed code keeps its
    -- hash and is never re-embedded.
    content_hash TEXT NOT NULL,
    UNIQUE (file_id, ordinal)
);

CREATE INDEX idx_chunks_file ON chunks(file_id);
CREATE INDEX idx_chunks_hash ON chunks(content_hash);

-- chunks_vec: the semantic lane. rowid == chunks.id (vec0 requires an INTEGER
-- rowid and cannot appear in triggers or FK cascades, so the store deletes
-- matching rows in the same transaction as the chunks delete).
CREATE VIRTUAL TABLE chunks_vec USING vec0(
    embedding float[1536]
);

-- chunks_fts: the keyword lane over the RAW text, keyed 1:1 by rowid ==
-- chunks.id. Standalone, so it is mirror-managed in the same transaction.
-- Hybrid search fuses this with the vec0 neighbours: PromoMailJob is found here
-- literally, "Teaser-Mail" is found by the vector lane semantically.
CREATE VIRTUAL TABLE chunks_fts USING fts5(raw_text);

-- embed_cache: content hash -> vector, so re-indexing unchanged content costs
-- nothing. In dev the whole corpus is re-indexed constantly; this is what keeps
-- that loop bearable, and it cuts the production diff-reindex bill too.
CREATE TABLE embed_cache (
    content_hash TEXT NOT NULL,
    model        TEXT NOT NULL,
    dim          INTEGER NOT NULL,
    embedding    BLOB NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (content_hash, model)
);

-- repo_deps: from the dependency manifests. Not embedded. This is the hard
-- signal that separates composition from ambiguity: if candidate A depends on
-- candidate B, they are parts of one mechanism and rongo must answer about all
-- of them instead of asking which one is meant.
CREATE TABLE repo_deps (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo       TEXT NOT NULL REFERENCES repo_state(name) ON DELETE CASCADE,
    coordinate TEXT NOT NULL,
    direction  TEXT NOT NULL CHECK (direction IN ('publishes', 'requires')),
    UNIQUE (repo, coordinate, direction)
);

CREATE INDEX idx_repo_deps_coordinate ON repo_deps(coordinate);
```

- [ ] **Step 2: Write the failing state test**

`backend/internal/indexer/state_test.go`:

```go
package indexer

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/rongo/internal/repos"
	"github.com/trick77/rongo/internal/store"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rongo.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSyncSpecs_insertsAndUpdates(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()

	// When
	err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true},
	})

	// Then
	if err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}
	if len(active) != 1 || active[0].Name != "peeq" {
		t.Fatalf("Active() = %+v, want one entry named peeq", active)
	}
}

func TestSyncSpecs_deactivatesRatherThanDeletes(t *testing.T) {
	// Given: peeq was indexed, then dropped out of repos.yaml. Its index must
	// survive — a typo in the YAML must not destroy hours of indexing.
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "master", Enabled: true},
	}); err != nil {
		t.Fatalf("first SyncSpecs() err = %v", err)
	}
	if err := s.MarkIndexed(ctx, "peeq", "abc123", Counts{Files: 10, Chunks: 100}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}

	// When: the list no longer mentions peeq.
	if err := s.SyncSpecs(ctx, nil); err != nil {
		t.Fatalf("second SyncSpecs() err = %v", err)
	}

	// Then: it leaves the active set ...
	active, err := s.Active(ctx)
	if err != nil {
		t.Fatalf("Active() err = %v", err)
	}
	if len(active) != 0 {
		t.Errorf("Active() = %+v, want empty after the repo left repos.yaml", active)
	}
	// ... but its row and its recorded work are still there.
	var enabled int
	var chunkCount int
	if err := db.QueryRow(
		`SELECT enabled, chunk_count FROM repo_state WHERE name = ?`, "peeq",
	).Scan(&enabled, &chunkCount); err != nil {
		t.Fatalf("row missing entirely, want it deactivated not deleted: %v", err)
	}
	if enabled != 0 {
		t.Errorf("enabled = %d, want 0", enabled)
	}
	if chunkCount != 100 {
		t.Errorf("chunk_count = %d, want the recorded 100 to survive", chunkCount)
	}
}

func TestMarkError_isVisibleAndCleared(t *testing.T) {
	// Given
	db := newDB(t)
	s := NewStateStore(db)
	ctx := context.Background()
	if err := s.SyncSpecs(ctx, []repos.Spec{
		{Name: "peeq", CloneURL: "/tmp/peeq", Branch: "release-2024.3", Enabled: true},
	}); err != nil {
		t.Fatalf("SyncSpecs() err = %v", err)
	}

	// When: the configured branch vanished upstream.
	if err := s.MarkError(ctx, "peeq", `branch "release-2024.3" not found`); err != nil {
		t.Fatalf("MarkError() err = %v", err)
	}

	// Then: it is visible on the state, not swallowed.
	active, _ := s.Active(ctx)
	if len(active) != 1 || active[0].LastError == "" {
		t.Fatalf("LastError = %q, want the branch failure surfaced", active[0].LastError)
	}

	// And a later success clears it, so a stale error cannot alarm forever.
	if err := s.MarkIndexed(ctx, "peeq", "def456", Counts{Files: 1, Chunks: 2}); err != nil {
		t.Fatalf("MarkIndexed() err = %v", err)
	}
	active, _ = s.Active(ctx)
	if active[0].LastError != "" {
		t.Errorf("LastError = %q, want it cleared after a successful run", active[0].LastError)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd backend && go test ./internal/indexer/ -v`
Expected: FAIL — `undefined: NewStateStore`

- [ ] **Step 4: Write the state store**

`backend/internal/indexer/state.go`:

```go
// Package indexer owns the indexing pipeline: keeping checkouts current,
// selecting files, extracting symbols, chunking, embedding and storing.
package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/trick77/rongo/internal/repos"
)

// Counts is what one indexing run produced.
type Counts struct {
	Files  int
	Chunks int
}

// RepoState is a repository's recorded indexing state.
type RepoState struct {
	Name      string
	CloneURL  string
	Branch    string
	Enabled   bool
	LastSHA   string
	LastError string
	LastRunAt time.Time
	Files     int
	Chunks    int
}

// StateStore reads and writes repo_state.
type StateStore struct {
	db *sql.DB
}

// NewStateStore builds a StateStore.
func NewStateStore(db *sql.DB) *StateStore {
	return &StateStore{db: db}
}

// SyncSpecs reconciles the database with the repository list. An entry present
// in the list is inserted or updated and enabled; an entry ABSENT from the list
// is deactivated, never deleted — its index survives until an explicit purge,
// because a typo in the YAML must not destroy hours of indexing.
func (s *StateStore) SyncSpecs(ctx context.Context, specs []repos.Spec) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE repo_state SET enabled = 0`); err != nil {
		return fmt.Errorf("deactivate all: %w", err)
	}
	for _, spec := range specs {
		enabled := 0
		if spec.Enabled {
			enabled = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO repo_state (name, clone_url, branch, enabled)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				clone_url = excluded.clone_url,
				branch    = excluded.branch,
				enabled   = excluded.enabled`,
			spec.Name, spec.CloneURL, spec.Branch, enabled,
		); err != nil {
			return fmt.Errorf("upsert %s: %w", spec.Name, err)
		}
	}
	return tx.Commit()
}

// Active lists the repositories currently in the list and enabled.
func (s *StateStore) Active(ctx context.Context) ([]RepoState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, clone_url, branch, enabled, last_sha, last_error, last_run_at,
		       file_count, chunk_count
		FROM repo_state WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RepoState
	for rows.Next() {
		var r RepoState
		var enabled int
		var lastRun string
		if err := rows.Scan(&r.Name, &r.CloneURL, &r.Branch, &enabled, &r.LastSHA,
			&r.LastError, &lastRun, &r.Files, &r.Chunks); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		if lastRun != "" {
			r.LastRunAt, _ = time.Parse(time.RFC3339, lastRun)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkIndexed records a successful run and clears any previous error, so a
// stale failure cannot alarm forever.
func (s *StateStore) MarkIndexed(ctx context.Context, name, sha string, c Counts) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE repo_state
		SET last_sha = ?, last_run_at = ?, last_error = '', file_count = ?, chunk_count = ?
		WHERE name = ?`,
		sha, time.Now().UTC().Format(time.RFC3339), c.Files, c.Chunks, name)
	return err
}

// MarkError records a failure so the Repos page can show it. A silent stop
// would freeze the index while everything looks healthy.
func (s *StateStore) MarkError(ctx context.Context, name, msg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE repo_state SET last_error = ?, last_run_at = ? WHERE name = ?`,
		msg, time.Now().UTC().Format(time.RFC3339), name)
	return err
}

// SetBranch records the branch actually in use, after the git layer resolved an
// omitted one from the remote.
func (s *StateStore) SetBranch(ctx context.Context, name, branch string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repo_state SET branch = ? WHERE name = ?`, branch, name)
	return err
}
```

- [ ] **Step 5: Write the scheduling helper**

`backend/internal/sched/sched.go` — ported in spirit from `peeq/backend/internal/sched/sched.go`; read that file first.

```go
// Package sched holds the loop primitives the background workers share:
// cancellable sleep and jittered intervals.
package sched

import (
	"context"
	"math/rand/v2"
	"time"
)

// Jittered spreads d by up to +/-20%, so several repositories polled on the same
// interval do not all hit their remote in the same second.
func Jittered(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := float64(d) * 0.2
	return time.Duration(float64(d) - spread + rand.Float64()*2*spread)
}

// Sleep waits for d or until ctx is done. It reports false if the context ended,
// so a caller can exit its loop without a second select.
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
```

- [ ] **Step 6: Write the poller test and poller**

`backend/internal/indexer/poller_test.go` must cover, with a local fixture repository and a fake indexing function:
- A repository whose HEAD is unchanged is NOT re-indexed (assert the index function was not called).
- A repository whose HEAD moved IS re-indexed, and receives only the changed paths.
- A repository with an unresolvable branch gets `MarkError` called and does NOT stop the poller from processing the next repository. Assert both.
- An omitted branch is resolved from the remote and written back with `SetBranch`.

`backend/internal/indexer/poller.go` implements `Poller` with:

```go
// Poller keeps every active repository current. It is deliberately sequential:
// indexing is IO- and API-bound, and a stampede of parallel clones against one
// forge is how a token gets rate-limited.
type Poller struct {
	state    *StateStore
	git      *gitrepo.Client
	index    IndexFunc
	interval time.Duration
	log      *slog.Logger
}

// IndexFunc indexes one repository at one commit. paths is nil for a full index
// and the changed paths for an incremental one.
type IndexFunc func(ctx context.Context, st RepoState, sha string, paths []string) (Counts, error)
```

`Run(ctx)` loops: `sched.Sleep(ctx, sched.Jittered(interval))`, then for each `Active` repo — resolve branch if empty, fetch, compare `HeadSHA` against `LastSHA`, and either skip, full-index (no `LastSHA`), or incremental-index (`ChangedPaths`). Every per-repository error calls `MarkError` and **continues to the next repository**; one broken remote must not stall the rest.

- [ ] **Step 7: Wire into main**

In `backend/cmd/rongo/main.go`, after the auth service: load `repos.Load(cfg.ReposFile)`, `SyncSpecs`, build the `gitrepo.Client` from `tools.Git` and `cfg.RepoRoot`, start the poller under a `sync.WaitGroup` with a cancellable context, and shut it down on signal. A missing `repos.yaml` is a **warning**, not a fatal: the server must still start so the operator can see the Repos page telling them the file is missing.

- [ ] **Step 8: Run tests and commit**

Run: `cd backend && go test ./... && go vet ./...`
Expected: PASS.

```
git add backend/internal/indexer backend/internal/sched backend/internal/store/migrations backend/cmd/rongo/main.go
git commit -m "feat: repository state, index migration and the poller"
```

---

### Task 4: File selection — what gets indexed at all

**Files:**
- Create: `backend/internal/indexer/select.go`
- Test: `backend/internal/indexer/select_test.go`

**Interfaces:**
- Produces: `indexer.Selector` with `Select(path string, body []byte) (Decision, string)`; `indexer.Decision` one of `Include`, `SkipVendored`, `SkipGenerated`, `SkipBinary`, `SkipTooLarge`, `SkipSecret`; `indexer.LanguageOf(path string) string`.

Filtering happens before embedding because unfiltered content costs money and dilutes every result list. The second reason matters more than the first: a vendored dependency's source outranks the actual answer for any query about a common word.

- [ ] **Step 1: Write the failing test**

The test table must cover, at minimum:

| Input | Expected |
|---|---|
| `src/cart/AbandonedCartJob.java` with normal code | `Include`, lang `java` |
| `node_modules/left-pad/index.js` | `SkipVendored` |
| `vendor/github.com/x/y/z.go` | `SkipVendored` |
| `ui/dist/assets/index-abc123.js` | `SkipGenerated` |
| `api/v1/service.pb.go` (contains `Code generated by protoc`) | `SkipGenerated` |
| `package-lock.json`, `go.sum`, `yarn.lock` | `SkipGenerated` |
| a body containing a NUL byte | `SkipBinary` |
| a 3 MB source file | `SkipTooLarge` |
| a body containing `AKIA` followed by 16 uppercase alphanumerics | `SkipSecret` |
| a body containing `-----BEGIN RSA PRIVATE KEY-----` | `SkipSecret` |
| a body containing `ghp_` followed by 36 alphanumerics | `SkipSecret` |

- [ ] **Step 2: Implement**

Key decisions the implementation must encode, each with a comment saying why:

- **Secret detection runs before embedding, not after.** Code leaves the network for embedding; an accidentally committed credential must not leave with it. A skipped file is recorded in `files` with its reason so the answer layer can say "this file exists but was not indexed" rather than pretending it does not exist — that is the "never invent" invariant applied to the index itself.
- Generated-file detection checks both the path (`dist/`, `build/`, `target/`, `.min.js`) and the first 2 KB of content for a `Code generated ... DO NOT EDIT` marker, because generators differ.
- The size ceiling is `BACKEND_INDEX_MAX_FILE_BYTES`, default 1 MB. A file above it is skipped whole rather than truncated: half a file produces confidently wrong answers about the other half.
- `LanguageOf` maps by extension only. It feeds `ctags` language selection and the chunker's comment syntax; a wrong guess degrades chunking, it does not break it.

- [ ] **Step 3: Run tests and commit**

```
git add backend/internal/indexer/select.go backend/internal/indexer/select_test.go
git commit -m "feat: file selection with vendored, generated, binary and secret filters"
```

---

### Task 5: Symbol extraction with universal-ctags

**Files:**
- Create: `backend/internal/symbols/ctags.go`
- Test: `backend/internal/symbols/ctags_test.go`

**Interfaces:**
- Consumes: `exttools.Paths.Ctags` (phase 1, already verified to be universal-ctags at startup).
- Produces: `symbols.NewExtractor(ctagsBin string) *Extractor`; `(*Extractor).Extract(ctx, path string, body []byte) ([]symbols.Symbol, error)`; `symbols.Symbol{Name, Kind, Scope string, Line int}`.

- [ ] **Step 1: Write the failing test**

Test against a real Java-shaped and a Go-shaped source string, asserting the extracted symbol names, kinds and lines. Include one file in a language `ctags` does not know, asserting `Extract` returns an empty slice and **no error** — that is the normal path, not a failure, and the chunker falls back to line windows.

- [ ] **Step 2: Implement**

Invoke `ctags` with `--output-format=json --fields=+neKzS --sort=no -f - -L -`, feeding the file via a temporary file (ctags needs a real path to infer the language). Parse the JSON lines. Each record yields name, kind, line and scope.

Two things the implementation must get right:
- **Never trust the binary is universal-ctags here.** Phase 1's `exttools.Resolve` already refused BSD ctags at startup, so this package may assume it — but it must still return a clear error if the JSON is unparseable, rather than silently returning zero symbols. Zero symbols and an unparseable output look identical downstream and mean opposite things.
- A `ctags` failure on ONE file must not fail the whole repository index. The caller logs and continues with line-window chunking for that file.

- [ ] **Step 3: Run tests and commit**

```
git add backend/internal/symbols
git commit -m "feat: symbol extraction via universal-ctags"
```

---

### Task 6: Chunking and enrichment

**Files:**
- Create: `backend/internal/indexer/chunk.go`
- Test: `backend/internal/indexer/chunk_test.go`

**Interfaces:**
- Consumes: `symbols.Symbol` (task 5).
- Produces: `indexer.Chunk{Ordinal, StartLine, EndLine int, Symbol, Text, RawText string, TokenCount int, ContentHash string}`; `indexer.ChunkFile(repo, branch, path string, body []byte, syms []symbols.Symbol, opts ChunkOptions) []Chunk`; `indexer.DefaultChunkOptions() ChunkOptions`.

**Read `peeq/backend/internal/rag/chunk.go` first** — the token estimator (~4 characters per token, deliberately not tiktoken, because a runtime vocabulary download is not acceptable) and the overlap handling port directly. Sizing target: ~600 tokens, 800 hard ceiling, ~75 overlap.

The part that is NOT a port, and is the heart of this task:

- [ ] **Step 1: Write the failing test**

Cover these cases explicitly:
- A file WITH symbols is cut at symbol boundaries: a chunk starts at a symbol's line and ends before the next symbol's line. Assert the boundaries against a fixture with three methods.
- A symbol whose body exceeds `MaxTokens` is split into several chunks with overlap, all carrying the same `Symbol` name.
- A file WITHOUT symbols falls back to line windows with overlap. Assert the windows overlap and cover the whole file.
- **The enriched `Text` differs from `RawText`.** `Text` must begin with the breadcrumb and the enclosing symbol names; `RawText` must be the source only. Assert both, because this is the whole reason the hybrid search works: a question in business language and a method body share almost no words, so the vector lane needs the enrichment, while the keyword lane must match the literal identifier.
- `ContentHash` is computed over `RawText` only, so re-formatting the breadcrumb does not invalidate the embedding cache — but changing the code does.
- Two identical bodies at different paths produce DIFFERENT hashes, because the breadcrumb is part of what was embedded. (Decide this deliberately and state it in a comment; the test pins whichever way it goes.)

- [ ] **Step 2: Implement**

The enriched text has this shape, and the comment in the code must say why each part is there:

```
repo/path/to/File.java
class AbandonedCartJob > method run
/** Sends the teaser mail for an abandoned cart. */
<the source body>
```

- [ ] **Step 3: Run tests and commit**

```
git add backend/internal/indexer/chunk.go backend/internal/indexer/chunk_test.go
git commit -m "feat: symbol-aware chunking with enriched embedding text"
```
---

### Task 7: Embedding client and the content-hash cache

**Files:**
- Create: `backend/internal/embed/client.go`
- Create: `backend/internal/embed/cache.go`
- Modify: `backend/internal/config/config.go`, `.env.example`
- Test: `backend/internal/embed/client_test.go`, `backend/internal/embed/cache_test.go`

**Interfaces:**
- Produces: `embed.NewClient(cfg embed.Config, hc *http.Client) *Client`; `(*Client).Embed(ctx, texts []string) ([][]float32, error)`; `embed.NewCache(db *sql.DB, model string, dim int) *Cache`; `(*Cache).Get(ctx, hashes []string) (map[string][]float32, error)`; `(*Cache).Put(ctx, hash string, vec []float32) error`; config fields `EmbedBaseURL, EmbedAPIKey, EmbedModel string`, `EmbedDim int` from `BACKEND_EMBED_*`.

**Read `peeq/backend/internal/rag/embed.go` first.** The OpenAI-compatible request shape, batching, the error-body cap and the heartbeat all port directly. Do not reinvent them.

- [ ] **Step 1: Write the failing client test**

Use `httptest` — no test may reach a real endpoint. Cover:
- A batch of three texts produces one request whose JSON body carries all three inputs, and returns three vectors **in input order**. Assert the order explicitly by having the fake return distinguishable vectors: out-of-order results silently mismatch every chunk with someone else's embedding, and nothing downstream would notice.
- A returned vector whose length differs from `BACKEND_EMBED_DIM` is an error, not a silently stored short vector — `vec0` would reject it later at a point far from the cause.
- A non-2xx response returns an error containing the status and a **capped** slice of the body.
- A context cancellation mid-request returns promptly.

- [ ] **Step 2: Write the failing cache test**

- `Get` returns only the hashes it holds, leaving the rest for the caller to embed. Assert a partial hit returns exactly the hit subset.
- `Put` then `Get` round-trips a vector bit-for-bit — encode as a little-endian float32 BLOB and assert the decoded values are identical, not merely close.
- The cache is keyed by `(content_hash, model)`: the same hash under a different model is a MISS. This is what makes the small-vs-large A/B in task 10 honest — without it the second model would silently reuse the first model's vectors.

- [ ] **Step 3: Implement both**

The cache is the reason a dev re-index is bearable: the corpus is ~2.6M tokens and re-embedding it on every run wastes minutes, not money (the whole corpus costs about five cents at `text-embedding-3-small`'s $0.02/1M). Moved or renamed code keeps its hash and is never re-embedded.

- [ ] **Step 4: Add config and document it**

`BACKEND_EMBED_BASE_URL`, `BACKEND_EMBED_API_KEY`, `BACKEND_EMBED_MODEL` (default `text-embedding-3-small`), `BACKEND_EMBED_DIM` (default 1536). Validate in `config.Load` that a configured dim is positive and that a base URL is present when indexing is enabled.

- [ ] **Step 5: Run tests and commit**

```
git add backend/internal/embed backend/internal/config .env.example
git commit -m "feat: embedding client with a content-hash cache"
```

---

### Task 8: The write path — storing chunks, vectors and keywords atomically

**Files:**
- Create: `backend/internal/indexer/write.go`
- Create: `backend/internal/indexer/index.go`
- Test: `backend/internal/indexer/write_test.go`, `backend/internal/indexer/index_test.go`

**Interfaces:**
- Produces: `indexer.NewWriter(db) *Writer`; `(*Writer).ReplaceFile(ctx, repo, path, sha, lang string, chunks []Chunk, vecs [][]float32, syms []symbols.Symbol) error`; `(*Writer).DeleteFile(ctx, repo, path string) error`; `indexer.New(deps Deps) *Indexer` with `IndexRepo(ctx, st RepoState, sha string, paths []string) (Counts, error)`.

- [ ] **Step 1: Write the failing write test**

The properties that matter, each its own test:
- `ReplaceFile` writes `files`, `chunks`, `chunks_vec` and `chunks_fts` **in one transaction**. Assert that a failure partway (inject one by passing a vector of the wrong dimension) leaves NONE of them written — a half-written file means the vector lane and the keyword lane disagree about what exists, and every later result set is quietly wrong.
- Re-indexing the same path REPLACES its rows rather than accumulating them. Assert the chunk count after two runs equals one run's.
- `chunks_vec` and `chunks_fts` rowids stay equal to `chunks.id`. Assert by joining all three. vec0 cannot participate in triggers or FK cascades, so this 1:1 bridging is maintained by hand and is exactly the thing that rots silently.
- `DeleteFile` removes the chunk rows from all three tables. Assert all three, not just `chunks` — an orphaned vector row would keep answering queries about deleted code.

- [ ] **Step 2: Write the failing pipeline test**

`IndexRepo` against the local fixture repository, with a fake embedder returning deterministic vectors:
- A full index (`paths == nil`) walks every selected file.
- An incremental index with two changed paths touches only those two, and a deleted path removes its rows.
- A file that fails `ctags` still gets indexed via line windows — assert chunks exist for it.
- A file skipped by the selector is recorded in `files` with its skip reason and has zero chunks, rather than being absent. This is the "never invent" invariant at the index level: the answer layer must be able to say "that file exists but was not indexed".

- [ ] **Step 3: Implement**

Order inside `IndexRepo`: list or diff paths → read each at the SHA → select → extract symbols → chunk → hash → cache lookup → embed the misses → write. Batch embedding calls; the client already batches, but the pipeline must not call it once per chunk.

- [ ] **Step 4: Run tests and commit**

```
git add backend/internal/indexer
git commit -m "feat: transactional write path and the indexing pipeline"
```

---

### Task 9: Hybrid retrieval

**Files:**
- Create: `backend/internal/retrieve/store.go`
- Create: `backend/internal/retrieve/query.go`
- Create: `backend/internal/retrieve/hybrid.go`
- Test: one test file per source file

**Interfaces:**
- Produces: `retrieve.New(db, embedder) *Retriever`; `(*Retriever).Search(ctx, q Query) ([]Hit, error)`; `retrieve.Query{Text string, Repos []string, K int}`; `retrieve.Hit{Repo, Branch, Path, Symbol, RawText string, StartLine, EndLine int, Score float64, Lanes []string}`.

**Read these three peeq files before writing anything**, and port rather than reinvent:
- `peeq/backend/internal/rag/store.go` — the `vec0` KNN query. Note the `k = ?` constraint must sit alongside the `MATCH`, and the distance bound is a post-filter on the KNN's output, not a predicate inside it.
- `peeq/backend/internal/rag/query.go` — the FTS5 MATCH builder. It escapes rather than strips, and returns a **ladder** of progressively looser rungs, each carrying its own confidence weight.
- `peeq/backend/internal/rag/hybrid.go` — `FuseWeighted`, Reciprocal Rank Fusion with `k = 60` and per-lane weights.

**The adaptation list — what changes for rongo:**

1. **Lane weighting is the point, and rongo needs it more than peeq does.** Unweighted RRF is rank-blind, so semantic noise outranks a literal keyword match. In rongo the literal lane is the one that finds `PromoMailJob` when the user typed the identifier; the semantic lane is what finds it when they typed "Teaser-Mail". Keep peeq's arrangement — the keyword lane weighted above the semantic one, and each rung of the ladder carrying its own weight.
2. **Two query embeddings, not one.** The spec calls for embedding the expanded query twice — once in business phrasing, once with guessed code vocabulary — and merging both result lists. In this phase the expansion step does not exist yet (it is phase 4's non-Pro call), so `Search` takes the query text as given and embeds it once. Structure `Search` so a caller can pass several query texts later without reshaping it: take `[]string` internally even if the exported `Query` carries one string today.
3. **Repository filter as a pre-filter.** vec0 treats `rowid IN (...)` as a first-class pre-filter; applying the repo restriction afterwards would let one repository's chunks consume the whole top-k and return nothing for the others. Port peeq's filter approach, and write the test that pins it: a fixture whose global top-k is entirely one repository must still return the other repository's chunk when that repository is the one requested.
4. **Every hit carries repo, branch, path and lines**, because every claim must be citable. Join through `files` and `repo_state` in the retrieval query rather than looking them up later.

- [ ] **Step 1: Write the failing tests**

Beyond the ports' own behaviour, pin these:
- A query matching a literal identifier exactly returns that chunk first, even when several chunks are semantically closer. This is the lane-weighting property and it must fail if the weights are flattened.
- A query with no matches anywhere returns an empty slice and NO error. "No hit means no hit" — the caller reports it with the terms tried; an error here would be indistinguishable from a broken database.
- The repository pre-filter test from adaptation 3.
- Every returned hit has a non-empty repo, path and a positive line range.

- [ ] **Step 2: Implement, then commit**

```
git add backend/internal/retrieve
git commit -m "feat: hybrid vector and keyword retrieval with weighted RRF"
```

---

### Task 10: Measurement — small vs large, and is retrieval any good

**Files:**
- Create: `backend/internal/retrieve/eval/questions.json`
- Create: `backend/internal/retrieve/eval/eval_test.go`
- Create: `docs/measurements/2026-XX-XX-embedding-model.md`

This task answers the question the spec deliberately deferred: `text-embedding-3-small` or `-large`. It is decided by measurement, not argument. The cost side is already settled — the whole dev corpus embeds for about five cents either way — so the decision rests entirely on retrieval quality.

- [ ] **Step 1: Build the question set**

`questions.json` holds 20-30 entries against the dev corpus (peeq, loom and the two dependencies), each shaped:

```json
{
  "question": "How is it prevented that two migrations run at the same time?",
  "expect_repo": "peeq",
  "expect_paths": ["backend/internal/store/migrate.go"],
  "kind": "how"
}
```

Write them from what the corpus actually contains — read the code and write questions whose answers you have verified, in the mix a real user produces: some in business language, some naming an identifier, some spanning two repositories. A question whose expected path you guessed makes the whole measurement worthless.

- [ ] **Step 2: Write the harness**

`eval_test.go` is skipped unless `BACKEND_EVAL=1`, because it needs a real embedding endpoint and a populated index. It reports, per model:
- **recall@k** for k of 5 and 20 — did the expected path appear at all?
- **MRR** — how far down was it?
- Per-question failures, listed, so a bad question is distinguishable from bad retrieval.

- [ ] **Step 3: Run the comparison**

Index the dev corpus twice, once per model, into separate database files. The cache is keyed by `(content_hash, model)`, so the second run genuinely re-embeds rather than reusing the first model's vectors.

- [ ] **Step 4: Write up the result and decide**

`docs/measurements/` records both models' numbers, the question set's size, the date, and the decision with its reasoning. Update the spec's deferred-decisions section to point at it. If the difference is inside the noise, choose `small` and say so explicitly — a tie goes to the cheaper and faster option, and recording that reasoning stops the question being reopened every quarter.

- [ ] **Step 5: Commit**

```
git add backend/internal/retrieve/eval docs/measurements
git commit -m "test: retrieval evaluation harness and the embedding model decision"
```

---

## Definition of done for phase 2

- [ ] `make test` and `make fe-test` pass; `CGO_ENABLED=0 go test ./...` passes.
- [ ] `repos.yaml` drives the corpus; no credential appears in it or in any log line.
- [ ] A repository removed from the list is deactivated with its index intact.
- [ ] A vanished branch shows as a loud error on the repository state, not a silent stop.
- [ ] A second run over an unchanged repository performs zero embedding calls (cache hit rate 100%).
- [ ] A push touching one file re-indexes one file, verified against the local fixture.
- [ ] Hybrid search returns a literal identifier match ahead of semantically closer noise.
- [ ] An empty result set is an empty slice, never an error.
- [ ] The embedding-model decision is recorded in `docs/measurements/` with its numbers.
- [ ] Branch `feat/phase-2-indexing` pushed with a PR open against `master`.

## Deliberately not in this phase

Feature cards and the clustering that produces them (phase 3), the ask pipeline, SSE and the clarification round-trip (phase 4), cost tracking (phase 5), history documents (phase 6). Phase 2 ends with a retriever that works and is measured, and no user-facing surface beyond a debug endpoint.
