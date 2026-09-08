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
	// Project names the product this repository is part of, and is REQUIRED.
	// It is the unit a reader is asked to choose between, so a repository that
	// stands alone is a project of one, conventionally named after itself.
	Project string
	// Kind is a free-form token for the part this repository plays — backend,
	// ui, consumer, contract. Deliberately not an enum: a closed vocabulary
	// rejects a real corpus the first time it needs a word not on the list, and
	// nothing branches on this deterministically. It reaches the answer prompt,
	// where a model reads "consumer" perfectly well.
	Kind string
	// Description is one human-written sentence saying what this repository
	// does. It is what separates a Kafka receiver from a second HTTP backend,
	// which Kind alone cannot. Never embedded, never indexed, never cited.
	Description string
	// Uses names sibling repositories INSIDE the same project that this one
	// depends on — declared by the consumer, the same direction as go.mod's
	// require. Coupling across projects is repo_deps' business, read from a
	// manifest rather than declared by hand.
	Uses []string
}

type file struct {
	Repositories []rawSpec `yaml:"repositories"`
}

// rawSpec exists so Enabled can default to true. A plain bool would default to
// false and silently disable every entry that omits the field.
type rawSpec struct {
	Name        string   `yaml:"name"`
	CloneURL    string   `yaml:"clone_url"`
	Branch      string   `yaml:"branch"`
	TokenEnv    string   `yaml:"token_env"`
	Enabled     *bool    `yaml:"enabled"`
	Project     string   `yaml:"project"`
	Kind        string   `yaml:"kind"`
	Description string   `yaml:"description"`
	Uses        []string `yaml:"uses"`
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
	// A list that names nothing is refused rather than returned empty, and this
	// is the floor under the purge: a repository absent from the list loses its
	// index and its checkout, so "the file parsed and mentions no repository"
	// would wipe the whole corpus. The ways to reach that state are not exotic —
	// a truncated file after a botched deploy, or `repos:` typed for
	// `repositories:`, which yaml.v3 accepts and silently reads as no entries.
	// Refusing here puts the caller on its "list unavailable" path, which leaves
	// everything exactly as it was and says so.
	if len(f.Repositories) == 0 {
		return nil, fmt.Errorf(
			"%s names no repository: expected a `repositories:` list with at least one entry", path)
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

		if strings.TrimSpace(r.Project) == "" {
			return nil, fmt.Errorf(
				"%s: project is required — rongo searches a project, and a repository that stands alone is a project of one named after itself",
				r.Name)
		}

		enabled := true
		if r.Enabled != nil {
			enabled = *r.Enabled
		}
		specs = append(specs, Spec{
			Name:        r.Name,
			CloneURL:    strings.TrimSpace(r.CloneURL),
			Branch:      strings.TrimSpace(r.Branch),
			TokenEnv:    strings.TrimSpace(r.TokenEnv),
			Enabled:     enabled,
			Project:     strings.TrimSpace(r.Project),
			Kind:        strings.TrimSpace(r.Kind),
			Description: strings.TrimSpace(r.Description),
			Uses:        trimAll(r.Uses),
		})
	}

	// Cross-entry checks come after every entry is read: each one needs the
	// whole list, and a uses edge cannot be judged against repositories the
	// loop has not reached yet.
	if err := validateProjects(specs); err != nil {
		return nil, err
	}
	return specs, nil
}

// trimAll copies a YAML string list with each entry trimmed, dropping empties.
// nil in, nil out: an absent `uses` and an empty one mean the same thing.
func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// validateProjects enforces the three rules that need the whole list.
func validateProjects(specs []Spec) error {
	project := make(map[string]string, len(specs)) // repository -> its project
	for _, s := range specs {
		project[s.Name] = s.Project
	}

	// A project name shares one namespace with repository names, because a card
	// button carries one of each. Naming a project after a repository that is
	// NOT its member makes that button mean two things. Naming it after its own
	// only member is the ordinary single-repository setup and is fine.
	for _, s := range specs {
		if p, ok := project[s.Project]; ok && p != s.Project {
			return fmt.Errorf(
				"%s: project %q is also the name of a repository that is not in it — project and repository names share one namespace",
				s.Name, s.Project)
		}
	}

	// A uses edge stays inside one product: the target must exist, sit in the
	// same project, and not be the entry itself. A cycle between siblings is
	// allowed — two backends calling each other is real, and layoutFlow removes
	// back edges by DFS before it ranks.
	for _, s := range specs {
		for _, u := range s.Uses {
			switch {
			case u == s.Name:
				return fmt.Errorf("%s: uses names itself", s.Name)
			case project[u] == "":
				return fmt.Errorf("%s: uses names %q, which is not a repository in this file", s.Name, u)
			case project[u] != s.Project:
				return fmt.Errorf(
					"%s: uses names %q, which is in project %q not %q — coupling across projects is read from a manifest, never declared here",
					s.Name, u, project[u], s.Project)
			}
		}
	}

	// Two branches of one repository inside one project would search the same
	// file at two commits and answer as one product. AGENTS.md already forbids
	// two cards differing only by branch; this is the same rule one level up.
	type pair struct{ project, cloneURL string }
	first := make(map[pair]string, len(specs))
	for _, s := range specs {
		k := pair{s.Project, s.CloneURL}
		if other, ok := first[k]; ok {
			return fmt.Errorf(
				"%s and %s are the same clone_url in project %q — a project cannot hold two branches of one repository",
				other, s.Name, s.Project)
		}
		first[k] = s.Name
	}
	return nil
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
