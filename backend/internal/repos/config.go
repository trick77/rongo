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
