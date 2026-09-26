// Package config loads rongo's runtime configuration from environment
// variables. Every setting is BACKEND_*; secrets come from the environment only.
package config

import (
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// AuthMode selects how rongo identifies a caller.
type AuthMode string

const (
	// AuthModeDev auto-logs in a fixed admin. Loopback addresses only.
	AuthModeDev AuthMode = "dev"
	// AuthModeToken gates every request on a shared bearer token.
	AuthModeToken AuthMode = "token"
	// AuthModePassword signs one admin in through a login form: the account
	// is BACKEND_ADMIN_USER, the password is checked against the bcrypt hash
	// in BACKEND_ADMIN_PASSWORD_HASH, and a session cookie carries the rest.
	AuthModePassword AuthMode = "password"
	// AuthModeOIDC is the production mode. The seam exists in phase 1; the
	// implementation lands later.
	AuthModeOIDC AuthMode = "oidc"
	// AuthModeProxy trusts X-Forwarded-User from an authenticating reverse
	// proxy in front of the process (an oauth-proxy sidecar, for one).
	// Loopback addresses only: the header is the whole credential.
	AuthModeProxy AuthMode = "proxy"
)

// Config holds all runtime settings.
type Config struct {
	Addr     string // HTTP listen address
	DBPath   string // path to the single SQLite file
	RepoRoot string // where rongo clones the repositories it indexes
	// ReposFile is the path to the repository list. Its entries carry no
	// secrets: tokens are named by token_env and read from the environment,
	// because that file ends up in a repository or a ticket eventually.
	ReposFile string
	// IndexMaxFileBytes is the ceiling above which a file is skipped WHOLE
	// rather than truncated: half a file produces confidently wrong answers
	// about the other half.
	IndexMaxFileBytes int
	// IndexMaxDataFileBytes is the ceiling for json and xml only: above it a
	// file is an export, a fixture or a translation, not configuration, and
	// its bulk outranks the real answer for every value it holds. Manifests
	// (pom.xml, package.json, project.json, ...) are exempt: the repository's
	// structure is read from them. BACKEND_INDEX_MAX_DATA_FILE_BYTES.
	IndexMaxDataFileBytes int
	// IndexMaxSchemaFileBytes is the ceiling for xsd and wsdl: a schema is a
	// hand-written contract, so it is not held to the data ceiling. Above it
	// sits a code table. BACKEND_INDEX_MAX_SCHEMA_FILE_BYTES.
	IndexMaxSchemaFileBytes int
	// HistoryDepth is how many first-parent commits a full index records
	// for the commit lane. BACKEND_HISTORY_DEPTH.
	HistoryDepth int
	// IndexEnabled switches the whole indexing side off, for a deployment that
	// only serves the UI. It defaults to ON. The embedding endpoint is
	// mandatory either way: the query side of every answer embeds the
	// question, and an indexer that cannot embed produces a repository list
	// that looks configured and an index that stays empty.
	IndexEnabled bool
	// IndexComments keeps whole-line comments in the text that is embedded and
	// full-text indexed. Setting BACKEND_INDEX_COMMENTS=0 leaves only code in
	// the search lanes; the source itself is stored untouched either way, so a
	// citation always quotes the real file.
	//
	// Changing this changes every chunk's content hash, so flipping it costs a
	// full re-embed of the corpus. That is intended: reusing vectors computed
	// with comments under a setting that excludes them would be silently wrong.
	IndexComments bool
	// IndexExclude lists repo-relative path globs whose files are recorded as
	// skipped ("excluded") instead of embedded: content written for reading,
	// not for the corpus — design documents, plans, mock-ups — that is stale
	// or wrong as an answer to how the code works. "**" spans directories;
	// patterns are anchored at the repository root. BACKEND_INDEX_EXCLUDE,
	// comma-separated; unset or empty is the default, "none" excludes nothing.
	// Already-indexed matches are swept at the next start; removing a pattern
	// takes effect for a file when it next changes.
	IndexExclude []string
	// GitSSHKey and GitSSHKnownHosts are the private key and host-key file git
	// uses for an ssh remote. Both or neither: the container has no home
	// directory to fall back on, and a bare host that leaves them unset keeps
	// its own agent and ~/.ssh. BACKEND_GIT_SSH_KEY, BACKEND_GIT_SSH_KNOWN_HOSTS.
	GitSSHKey        string
	GitSSHKnownHosts string
	// GitCAFile is a PEM bundle git trusts for https remotes in place of the
	// system store, for a forge behind an internal CA. BACKEND_GIT_CA_FILE.
	GitCAFile string
	// ModuleMinChunks and ModuleMaxChunks are the module cut: below the first a
	// directory is folded into its parent, above the second it is split again.
	// Calibrated against the real corpus and recorded in the measurement
	// document — the defaults here are a starting point, not a finding.
	ModuleMinChunks int
	ModuleMaxChunks int
	// RouteMargin is how far the leading candidate must be ahead before a
	// turn answers without asking. The phase 4b routing measurement
	// (docs/measurements/2026-08-18-routing.md) swept this value and found
	// that LOWER margins score better on its accuracy table — but only
	// because asking less scores better on a catalogue that is 80% "do not
	// ask", and the best-scoring value (0.10) would optimise the router
	// towards switching itself off. The default stays at 0.25 despite the
	// sweep, pending a fix to the candidate layer (phase 4c). The number to
	// beat is 0.803: a router that never asks anything at all.
	RouteMargin float64
	// No model endpoint is here: hosts and keys are llmwire's, carried by
	// each model's profile and the LLMWIRE_* variables llmwire reads itself
	// when internal/llm builds its client, so a missing key is a boot error
	// there. What a deployment may choose is the MODEL, by llmwire profile
	// id: LLMModel answers, LLMGateModel is the lane whose output is an id
	// or a label (routing, follow-ups, titles). Both required: rongo has no
	// model of its own. Checked against llmwire's registry in main, not here:
	// config stays a stdlib-only leaf.
	LLMModel     string
	LLMGateModel string
	// The call policy: what a pinned gate call sends as temperature (nil =
	// none), and how long one call may take. Reasoning is not here: each call
	// states its intent and llmwire renders it for the model. Malformed values
	// fail the boot: a typo here changes every routing decision, and is not a
	// tunable that may quietly fall back.
	LLMGateTemperature *float64
	LLMTimeout         time.Duration
	// GatherMaxHops and GatherTokenBudget bound the reference walk. Without
	// them one question walks the corpus.
	// LocateRounds is how many times the locate loop may look again after the
	// walk; 0 switches it off. See ask.Gatherer.WithLocateLoop.
	LocateRounds      int
	GatherMaxHops     int
	GatherTokenBudget int
	// TurnMaxTokens is the most one turn may spend across its model and
	// embedding calls. A tripwire, not a budget: turns measure 24–37k, the
	// heaviest on record 83k, and the default sits three times above that so
	// normal operation never meets it. Zero turns it off.
	TurnMaxTokens int
	// Memory keeps each reader's standing instructions ("never show me
	// flowcharts") across threads and writes them into every answer prompt.
	// Off, the understanding step asks for none and the page says so.
	Memory bool
	// No embedding model here: it is embed.Model, a constant of the build,
	// and its width embed.Dim comes from the llmwire profile. Its host is the
	// profile's and its key, LLMWIRE_OPENAI_API_KEY, is read by llmwire the
	// same way as the chat one.
	AuthMode   AuthMode
	AdminToken string // required when AuthMode is token
	// The password block, both required when AuthMode is password. The hash
	// is bcrypt, produced by `rongo -hash-password`; the plaintext is never
	// read from the environment.
	AdminUser         string
	AdminPasswordHash string
	SessionSecret     string // reserved: not read by anything yet — see the check below
	LogLevel          string
	// The OIDC block, all required when AuthMode is oidc. The issuer carries no
	// path and no trailing slash for Authelia (https://auth.trick77.com);
	// anything else fails discovery.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	// OIDCAdminGroup is the group whose members are admins. Empty means no
	// group check: everyone the provider let through is an admin, which is the
	// truthful default while the only real gate is Authelia's
	// authorization_policy.
	OIDCAdminGroup string
	// CookieSecure marks the session and nonce cookies Secure. In oidc mode
	// it is derived from OIDCRedirectURL: behind a TLS-terminating proxy the
	// process only ever sees plain HTTP, and the redirect URL is the one
	// setting that has to name the external origin anyway. Password mode has
	// no such URL, so it reads BACKEND_COOKIE_SECURE, default true, and only
	// a loopback listener may switch it off.
	CookieSecure bool
}

// retiredEnv are variables a deployment may still carry that no longer do
// anything. Set, they refuse the boot: a setting that looks active and is
// ignored is the failure rongo refuses everywhere else.
var retiredEnv = []string{"BACKEND_LLM_GATE_REASONING", "BACKEND_LLM_REASONING"}

// Load reads and validates the environment. It returns the first problem it
// finds rather than starting a half-configured server.
func Load() (Config, error) {
	r := &envReader{}
	cfg := Config{
		Addr:      envOr("BACKEND_ADDR", "127.0.0.1:8080"),
		DBPath:    envOr("BACKEND_DB_PATH", "./data/rongo.db"),
		RepoRoot:  envOr("BACKEND_REPO_ROOT", "./repos"),
		ReposFile: envOr("BACKEND_REPOS_FILE", "./repos.yaml"),
		// 1 MiB. A source file above that is machine-written or a data blob,
		// not something a person asks how it works.
		IndexMaxFileBytes: r.intOr("BACKEND_INDEX_MAX_FILE_BYTES", 1<<20),
		// 8 KiB. The json and xml that answer questions (tsconfig, a small
		// fixture) stay under 1 KB; a data blob or a translation catalogue
		// starts at 30 KB. Measured in docs/measurements/2026-09-17-data-file-cap.md.
		IndexMaxDataFileBytes: r.intOr("BACKEND_INDEX_MAX_DATA_FILE_BYTES", 8<<10),
		// 256 KiB: the syrius service schemas run to 69 KB; what stays above
		// is two code tables and one VO catalogue (2026-09-17-data-file-cap.md,
		// Schemas).
		IndexMaxSchemaFileBytes: r.intOr("BACKEND_INDEX_MAX_SCHEMA_FILE_BYTES", 256<<10),
		// 500 commits: a year of a busy repository, bounded for a monorepo's
		// first run. A "what changed" question looks back a year at most.
		HistoryDepth:     r.intOr("BACKEND_HISTORY_DEPTH", 500),
		IndexEnabled:     r.boolOr("BACKEND_INDEX_ENABLED", true),
		IndexComments:    r.boolOr("BACKEND_INDEX_COMMENTS", true),
		IndexExclude:     envListOr("BACKEND_INDEX_EXCLUDE", []string{"docs/plans/**"}),
		GitSSHKey:        strings.TrimSpace(os.Getenv("BACKEND_GIT_SSH_KEY")),
		GitSSHKnownHosts: strings.TrimSpace(os.Getenv("BACKEND_GIT_SSH_KNOWN_HOSTS")),
		GitCAFile:        strings.TrimSpace(os.Getenv("BACKEND_GIT_CA_FILE")),
		ModuleMinChunks:  r.intOr("BACKEND_MODULE_MIN_CHUNKS", 8),
		ModuleMaxChunks:  r.intOr("BACKEND_MODULE_MAX_CHUNKS", 150),
		RouteMargin:      r.floatOr("BACKEND_ROUTE_MARGIN", 0.25),
		// The locate loop, on at three rounds: look, narrow, confirm. opencode
		// answered the flagship in 38 calls; one round of six never had room
		// to read what its grep showed. 0 switches it off, and more than
		// three is capped.
		LocateRounds:      r.intOrOff("BACKEND_LOCATE_ROUNDS", 3),
		GatherMaxHops:     r.intOr("BACKEND_GATHER_MAX_HOPS", 2),
		GatherTokenBudget: r.intOr("BACKEND_GATHER_TOKEN_BUDGET", 24000),
		TurnMaxTokens:     r.intOrOff("BACKEND_TURN_MAX_TOKENS", 250000),
		Memory:            r.boolOr("BACKEND_MEMORY", true),
		LLMModel:          strings.TrimSpace(os.Getenv("BACKEND_LLM_MODEL")),
		LLMGateModel:      strings.TrimSpace(os.Getenv("BACKEND_LLM_GATE_MODEL")),
		AuthMode:          AuthMode(envOr("BACKEND_AUTH_MODE", string(AuthModeDev))),
		AdminToken:        strings.TrimSpace(os.Getenv("BACKEND_ADMIN_TOKEN")),
		AdminUser:         strings.TrimSpace(os.Getenv("BACKEND_ADMIN_USER")),
		AdminPasswordHash: strings.TrimSpace(os.Getenv("BACKEND_ADMIN_PASSWORD_HASH")),
		SessionSecret:     strings.TrimSpace(os.Getenv("BACKEND_SESSION_SECRET")),
		LogLevel:          r.oneOf("BACKEND_LOG_LEVEL", "info", "debug", "info", "warn", "error"),
		// The issuer is trimmed of its trailing slash for the same reason the
		// endpoint URLs above are: a discovery URL built from
		// "https://auth.example.com/" gets a double slash and 404s.
		OIDCIssuer:       strings.TrimRight(strings.TrimSpace(os.Getenv("BACKEND_OIDC_ISSUER")), "/"),
		OIDCClientID:     strings.TrimSpace(os.Getenv("BACKEND_OIDC_CLIENT_ID")),
		OIDCClientSecret: strings.TrimSpace(os.Getenv("BACKEND_OIDC_CLIENT_SECRET")),
		OIDCRedirectURL:  strings.TrimSpace(os.Getenv("BACKEND_OIDC_REDIRECT_URL")),
		OIDCAdminGroup:   strings.TrimSpace(os.Getenv("BACKEND_OIDC_ADMIN_GROUP")),
	}
	if r.err != nil {
		return Config{}, r.err
	}
	for _, name := range retiredEnv {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return Config{}, fmt.Errorf("%s is no longer read: reasoning follows each call's intent, rendered for the model by llmwire; remove it", name)
		}
	}
	var err error
	if cfg.LLMGateTemperature, err = envOptionalFloat("BACKEND_LLM_GATE_TEMPERATURE", 0); err != nil {
		return Config{}, err
	}
	if cfg.LLMTimeout, err = envDurationOr("BACKEND_LLM_TIMEOUT", 15*time.Minute); err != nil {
		return Config{}, err
	}
	// SessionSecret is currently unused — sessions are 256-bit random tokens
	// stored as unsalted SHA-256, no signing involved yet. It is still
	// required so a later phase that adds cookie signing can assume the
	// value is real instead of finding every deployment signed with a
	// placeholder. "change-me" and anything under 16 characters are rejected
	// for the same reason.
	if cfg.SessionSecret == "" {
		return Config{}, fmt.Errorf("BACKEND_SESSION_SECRET is required")
	}
	if cfg.SessionSecret == "change-me" {
		return Config{}, fmt.Errorf(
			"BACKEND_SESSION_SECRET must not be the placeholder value %q; generate one with `openssl rand -base64 32`", "change-me")
	}
	if len(cfg.SessionSecret) < 16 {
		return Config{}, fmt.Errorf(
			"BACKEND_SESSION_SECRET must be at least 16 characters; generate one with `openssl rand -base64 32`")
	}

	if cfg.LLMModel == "" {
		return Config{}, fmt.Errorf("BACKEND_LLM_MODEL is required: the llmwire profile id that answers")
	}
	if cfg.LLMGateModel == "" {
		return Config{}, fmt.Errorf("BACKEND_LLM_GATE_MODEL is required: the llmwire profile id for routing, titles and follow-ups")
	}

	switch cfg.AuthMode {
	case AuthModeDev:
		if !isLoopback(cfg.Addr) {
			return Config{}, fmt.Errorf(
				"BACKEND_AUTH_MODE=dev signs in an admin without credentials and is only allowed on a loopback address, got BACKEND_ADDR=%q", cfg.Addr)
		}
	case AuthModeProxy:
		// Same door as dev mode: anyone who can reach the listener can write
		// the header, so the only caller allowed is the proxy on localhost.
		if !isLoopback(cfg.Addr) {
			return Config{}, fmt.Errorf(
				"BACKEND_AUTH_MODE=proxy trusts the X-Forwarded-User header and is only allowed on a loopback address, got BACKEND_ADDR=%q", cfg.Addr)
		}
	case AuthModeToken:
		if cfg.AdminToken == "" {
			return Config{}, fmt.Errorf("BACKEND_AUTH_MODE=token requires BACKEND_ADMIN_TOKEN")
		}
	case AuthModePassword:
		if cfg.AdminUser == "" {
			return Config{}, fmt.Errorf("BACKEND_AUTH_MODE=password requires BACKEND_ADMIN_USER")
		}
		if cfg.AdminPasswordHash == "" {
			return Config{}, fmt.Errorf(
				"BACKEND_AUTH_MODE=password requires BACKEND_ADMIN_PASSWORD_HASH; generate one with `rongo -hash-password`")
		}
		// A plaintext pasted here would fail every login with a generic 401
		// and look like a typo in the password. Refusing to boot names it.
		if _, err := bcrypt.Cost([]byte(cfg.AdminPasswordHash)); err != nil {
			return Config{}, fmt.Errorf(
				"BACKEND_ADMIN_PASSWORD_HASH is not a bcrypt hash; generate one with `rongo -hash-password`")
		}
		cfg.CookieSecure = r.boolOr("BACKEND_COOKIE_SECURE", true)
		// Read after the one check above, so its own malformed value is
		// refused like every other setting's.
		if r.err != nil {
			return Config{}, r.err
		}
		if !cfg.CookieSecure && !isLoopback(cfg.Addr) {
			return Config{}, fmt.Errorf(
				"BACKEND_COOKIE_SECURE=false sends the session cookie over plain HTTP and is only allowed on a loopback address, got BACKEND_ADDR=%q", cfg.Addr)
		}
	case AuthModeOIDC:
		// Every one of these is fatal rather than a warning. A half-configured
		// OIDC deployment starts, serves the SPA, and then refuses every login
		// with a callback error — which looks like a provider outage rather
		// than a missing environment variable.
		for _, m := range []struct {
			name  string
			value string
		}{
			{"BACKEND_OIDC_ISSUER", cfg.OIDCIssuer},
			{"BACKEND_OIDC_CLIENT_ID", cfg.OIDCClientID},
			{"BACKEND_OIDC_CLIENT_SECRET", cfg.OIDCClientSecret},
			{"BACKEND_OIDC_REDIRECT_URL", cfg.OIDCRedirectURL},
		} {
			if m.value == "" {
				return Config{}, fmt.Errorf("BACKEND_AUTH_MODE=oidc requires %s", m.name)
			}
		}
		// The session and the OIDC nonce cookies get their Secure flag from
		// the redirect URL alone. Behind a TLS-terminating proxy the process
		// only sees plain HTTP, so nothing else can notice that an operator
		// wrote http://; the login works and the cookies go out readable.
		if !strings.HasPrefix(strings.ToLower(cfg.OIDCRedirectURL), "https://") {
			return Config{}, fmt.Errorf(
				"BACKEND_AUTH_MODE=oidc requires an https BACKEND_OIDC_REDIRECT_URL, got %q; the session cookie's Secure flag is derived from it", cfg.OIDCRedirectURL)
		}
	default:
		return Config{}, fmt.Errorf("unknown BACKEND_AUTH_MODE %q (want dev, token, password, oidc or proxy)", cfg.AuthMode)
	}

	if cfg.AuthMode == AuthModeOIDC {
		cfg.CookieSecure = strings.HasPrefix(strings.ToLower(cfg.OIDCRedirectURL), "https://")
	}

	// A file named here that is not there fails the boot. ssh would report
	// it too, but only at the first poll of the first ssh remote, as a
	// per-repository error on the Repos page, which reads as that
	// repository's problem rather than this deployment's. A key without a
	// known_hosts is refused for the same reason: strict host-key checking
	// against no file fails every ssh remote.
	if cfg.GitSSHKey != "" && cfg.GitSSHKnownHosts == "" {
		return Config{}, fmt.Errorf("BACKEND_GIT_SSH_KEY requires BACKEND_GIT_SSH_KNOWN_HOSTS; ssh-keyscan the forge into it")
	}
	if cfg.GitSSHKnownHosts != "" && cfg.GitSSHKey == "" {
		return Config{}, fmt.Errorf("BACKEND_GIT_SSH_KNOWN_HOSTS requires BACKEND_GIT_SSH_KEY")
	}
	for _, f := range []struct{ name, path string }{
		{"BACKEND_GIT_SSH_KEY", cfg.GitSSHKey},
		{"BACKEND_GIT_SSH_KNOWN_HOSTS", cfg.GitSSHKnownHosts},
		{"BACKEND_GIT_CA_FILE", cfg.GitCAFile},
	} {
		if f.path == "" {
			continue
		}
		if _, err := os.Stat(f.path); err != nil {
			return Config{}, fmt.Errorf("%s: %w", f.name, err)
		}
	}

	return cfg, nil
}

// envListOr reads a comma-separated list. Unset or blank means the default,
// like every other setting; the literal "none" is how an operator switches
// the list off, since an empty value cannot say "nothing" here.
func envListOr(key string, fallback []string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	if strings.EqualFold(v, "none") {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// envReader reads the typed settings and keeps the first malformed one. Empty
// means the default; anything else that does not parse is an error, and Load
// refuses to start on it. A fallback here would let BACKEND_MEMORY=disabled
// leave memory on and BACKEND_ROUTE_MARGIN=0 route at 0.25, each looking
// right in the environment and wrong in every decision — the same reason an
// invalid repos.yaml refuses to boot.
type envReader struct {
	err error
}

func (r *envReader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// intOr reads a positive integer setting.
func (r *envReader) intOr(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		r.fail(fmt.Errorf("%s=%q is not a positive integer", key, v))
		return fallback
	}
	return n
}

// intOrOff is intOr for a limit that can be switched off: an explicit 0
// means off and is kept.
func (r *envReader) intOrOff(key string, fallback int) int {
	if strings.TrimSpace(os.Getenv(key)) == "0" {
		return 0
	}
	return r.intOr(key, fallback)
}

// floatOr reads a positive float setting.
func (r *envReader) floatOr(key string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		r.fail(fmt.Errorf("%s=%q is not a positive number", key, v))
		return fallback
	}
	return f
}

// boolOr reads an on/off setting.
func (r *envReader) boolOr(key string, fallback bool) bool { //nolint:unparam // a general on/off reader: every current setting happens to default to true, which is not a property of the function
	v := strings.TrimSpace(os.Getenv(key))
	switch strings.ToLower(v) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		r.fail(fmt.Errorf("%s=%q is not on or off; want true or false", key, v))
		return fallback
	}
}

// oneOf reads a setting that must be one of a fixed set of words, case
// folded, and returns it lower-cased.
func (r *envReader) oneOf(key, fallback string, allowed ...string) string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	r.fail(fmt.Errorf("%s=%q is not one of %s", key, v, strings.Join(allowed, ", ")))
	return fallback
}

// envOptionalFloat reads a float setting that may also be switched off:
// "default" (or "none") returns nil, meaning "send nothing". Malformed is an
// error, not a fallback: see Config.LLMGateTemperature.
func envOptionalFloat(key string, fallback float64) (*float64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	switch strings.ToLower(v) {
	case "":
		return &fallback, nil
	case "default", "none":
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("%s=%q is not a temperature; want a number >= 0, or default", key, v)
	}
	return &f, nil
}

// envDurationOr reads a Go duration ("15m", "90s"). Malformed is an error.
func envDurationOr(key string, fallback time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s=%q is not a duration; want e.g. 15m or 90s", key, v)
	}
	return d, nil
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
