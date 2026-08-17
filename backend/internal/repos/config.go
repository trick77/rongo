// Package repos loads and validates the repository list. The list lives in a
// YAML file rather than a database table so it is versionable and reviewable in
// a diff; credentials deliberately do NOT live there, because that file ends up
// in a repository or a ticket sooner or later.
package repos

import (
	"fmt"
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

// errCredential is the message used for every rejection: the token belongs in
// an env var named by token_env, never inline in the URL.
func errCredential(name string) error {
	return fmt.Errorf(
		"%s: clone_url must not embed credentials — put the token in an env var and name it with token_env",
		name)
}

// validateCloneURL refuses credentials embedded in the URL.
//
// net/url.Parse is deliberately NOT used to classify this: a string like
// "user:pass@host/path" (no "//" after the scheme) parses successfully as an
// OPAQUE URL with scheme="user", Host="" and User=nil, so a check built on
// u.User silently accepts exactly the credential-bearing strings this
// function exists to reject. Instead this walks the raw string structurally.
func validateCloneURL(name, raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%s: clone_url is required", name)
	}

	authority := authorityOf(trimmed)
	at := strings.Index(authority, "@")
	if at < 0 {
		return nil
	}
	userinfo := authority[:at]

	if strings.Contains(userinfo, ":") {
		// "user:password@host" — unambiguous userinfo credentials.
		return errCredential(name)
	}

	// No colon: structurally this is scp-style "user@host" (e.g.
	// git@github.com:acme/repo.git), which is a legitimate ssh remote and must
	// stay accepted. Still reject it when the "user" looks like a token rather
	// than a username. This is a heuristic backstop, not a guarantee —
	// "token@host" is structurally indistinguishable from "user@host", so the
	// real protection is token_env, not this check.
	if looksLikeCredential(userinfo) {
		return errCredential(name)
	}
	return nil
}

// authorityOf returns the URL's authority segment: everything after a
// "scheme://" up to the next '/', or the whole leading segment up to the
// first '/' when there is no "://" (covers scp-style and bare host:path
// forms, which net/url does not parse as authorities at all).
func authorityOf(raw string) string {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+len("://"):]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// credentialPrefixes are common forge-issued token shapes. Seeing one as the
// "user" in a scp-style URL means a token was pasted where a username goes.
var credentialPrefixes = []string{"ghp_", "gho_", "ghs_", "github_pat_", "glpat-", "xoxb-"}

// looksLikeCredential flags a userinfo string that reads as a token rather
// than a username: a known forge token prefix, or simply too long for a
// username (git@github.com is 3 chars; a token is typically 40+).
func looksLikeCredential(userinfo string) bool {
	if len(userinfo) > 40 {
		return true
	}
	for _, p := range credentialPrefixes {
		if strings.HasPrefix(userinfo, p) {
			return true
		}
	}
	return false
}
