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

func TestLoad_rejectsInlineSecretWithoutScheme(t *testing.T) {
	// Given: a "https://" prefix is easy to forget when pasting a basic-auth
	// snippet, and the URL still parses "successfully" as opaque without it.
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: "user:pass@github.com/acme/shop.git"
`)

	_, err := Load(path)

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal of credentials embedded in clone_url without a scheme")
	}
}

func TestLoad_rejectsOAuth2StyleTokenWithoutScheme(t *testing.T) {
	// Given: the "oauth2:TOKEN@host" form forges commonly hand out for
	// clone-with-token snippets, pasted without "https://".
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: "oauth2:ghp_realtoken@github.com:acme/shop.git"
`)

	_, err := Load(path)

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal of an oauth2:TOKEN@host clone_url")
	}
}

func TestLoad_rejectsBareTokenAsScpUser(t *testing.T) {
	// Given: no colon at all, but the "user" in the scp-style form is itself a
	// token — token@host is what's left after someone drops the password
	// separator by hand.
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: "ghp_realtoken@github.com:acme/shop.git"
`)

	_, err := Load(path)

	if err == nil {
		t.Fatal("Load() err = nil, want a refusal of a token used as the scp-style user")
	}
}

func TestLoad_acceptsScpStyleSSHRemote(t *testing.T) {
	// Given: git@host:org/repo.git is the ordinary ssh remote form and must
	// stay accepted — over-rejecting pushes people back toward embedding
	// tokens in https URLs instead.
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: "git@github.com:acme/repo.git"
`)

	specs, err := Load(path)

	if err != nil {
		t.Fatalf("Load() err = %v, want nil for a scp-style ssh remote", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
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
