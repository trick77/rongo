package repos

import (
	"os"
	"path/filepath"
	"strings"
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
    project: shop
  - name: commons-mail
    clone_url: https://forge.example.invalid/acme/commons-mail.git
    project: commons-mail
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
    project: shop
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

func TestLoad_aListThatNamesNothingIsAnError(t *testing.T) {
	// This is the floor under the purge: a repository absent from the list loses
	// its index and its checkout, so a file that parses to no entries would wipe
	// the corpus on the next boot. An error puts the caller on its "list
	// unavailable" path instead, which changes nothing.
	//
	// Both shapes reach it without anything looking wrong: a truncated file, and
	// `repos:` typed for `repositories:` — yaml.v3 ignores the unknown key and
	// reports no error at all.
	for _, body := range []string{
		"",
		"repositories:\n",
		"repos:\n  - name: shop\n    clone_url: https://forge.example.invalid/acme/shop.git\n",
	} {
		if _, err := Load(writeYAML(t, body)); err == nil {
			t.Errorf("Load(%q) err = nil, want a refusal of a list naming no repository", body)
		}
	}
}

func TestLoad_readsProjectStructure(t *testing.T) {
	// Given: one product in three repositories, with the wiring declared.
	path := writeYAML(t, `
repositories:
  - name: shop-ui
    clone_url: https://forge.example.invalid/acme/shop-ui.git
    project: shop
    kind: ui
    description: Customer-facing storefront, React.
    uses: [shop-backend]
  - name: shop-backend
    clone_url: https://forge.example.invalid/acme/shop-backend.git
    project: shop
    kind: backend
    description: Storefront API and checkout.
  - name: shop-events
    clone_url: https://forge.example.invalid/acme/shop-events.git
    project: shop
    kind: consumer
`)

	// When
	specs, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if specs[0].Project != "shop" || specs[0].Kind != "ui" {
		t.Errorf("specs[0] project/kind = %q/%q, want shop/ui", specs[0].Project, specs[0].Kind)
	}
	if specs[0].Description != "Customer-facing storefront, React." {
		t.Errorf("specs[0].Description = %q", specs[0].Description)
	}
	if len(specs[0].Uses) != 1 || specs[0].Uses[0] != "shop-backend" {
		t.Errorf("specs[0].Uses = %v, want [shop-backend]", specs[0].Uses)
	}
	// kind and description are optional, and so is uses: a consumer reached
	// from a queue declares no sibling at all.
	if len(specs[2].Uses) != 0 {
		t.Errorf("specs[2].Uses = %v, want empty", specs[2].Uses)
	}
	if specs[1].Description != "Storefront API and checkout." {
		t.Errorf("specs[1].Description = %q", specs[1].Description)
	}
}

func TestLoad_requiresProject(t *testing.T) {
	// Given: rongo searches a project, so every repository belongs to one. A
	// project of one is named after its own repository; there is no implicit
	// case left to guess at.
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: https://forge.example.invalid/acme/shop-backend.git
`)

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("Load() err = nil, want a refusal for a missing project")
	}
	if !strings.Contains(err.Error(), "project") {
		t.Errorf("Load() err = %v, want it to name the missing field", err)
	}
}

func TestLoad_allowsProjectNamedAfterItsOnlyMember(t *testing.T) {
	// Given: the ordinary single-repository setup, and rongo's own.
	path := writeYAML(t, `
repositories:
  - name: rongo
    clone_url: https://github.com/trick77/rongo.git
    project: rongo
`)

	// When
	specs, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil: a project of one is named after its member", err)
	}
	if specs[0].Project != "rongo" {
		t.Errorf("specs[0].Project = %q, want rongo", specs[0].Project)
	}
}

func TestLoad_rejectsProjectNamedAfterAnotherRepository(t *testing.T) {
	// Given: project and repository names share one namespace, because a card
	// button carries one of each. A project named after a repository that is
	// not its member makes that button mean two things.
	path := writeYAML(t, `
repositories:
  - name: shop-ui
    clone_url: https://forge.example.invalid/acme/shop-ui.git
    project: legacy-crm
  - name: legacy-crm
    clone_url: https://forge.example.invalid/acme/legacy-crm.git
    project: legacy-crm-suite
`)

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("Load() err = nil, want a refusal for a project colliding with another repository's name")
	}
}

func TestLoad_rejectsAProjectNamedAfterOneOfSeveralMembers(t *testing.T) {
	// Given: "shop" is both a repository and the product holding it. Naming the
	// repository would expand the search to shop-ui, so a thread that named the
	// narrower thing would widen — the one move the funnel forbids. A project of
	// one keeps the same name legitimately, which is why the count decides.
	path := writeYAML(t, `
repositories:
  - name: shop
    clone_url: https://forge.example.invalid/acme/shop.git
    project: shop
  - name: shop-ui
    clone_url: https://forge.example.invalid/acme/shop-ui.git
    project: shop
`)

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("Load() err = nil, want a refusal for a multi-repository project named after one of its own")
	}
}

func TestLoad_rejectsUsesOutsideTheProject(t *testing.T) {
	// Given: uses is an edge inside one product. Coupling across products is
	// repo_deps' business, read from a manifest rather than declared by hand.
	cases := map[string]string{
		"unknown repository": `
repositories:
  - name: shop-ui
    clone_url: https://forge.example.invalid/acme/shop-ui.git
    project: shop
    uses: [nowhere]
`,
		"another project": `
repositories:
  - name: shop-ui
    clone_url: https://forge.example.invalid/acme/shop-ui.git
    project: shop
    uses: [legacy-crm]
  - name: legacy-crm
    clone_url: https://forge.example.invalid/acme/legacy-crm.git
    project: legacy-crm
`,
		"itself": `
repositories:
  - name: shop-ui
    clone_url: https://forge.example.invalid/acme/shop-ui.git
    project: shop
    uses: [shop-ui]
`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			// When
			_, err := Load(writeYAML(t, body))

			// Then
			if err == nil {
				t.Fatalf("Load() err = nil, want a refusal for a uses entry naming %s", name)
			}
		})
	}
}

func TestLoad_allowsUsesCycleInsideAProject(t *testing.T) {
	// Given: two backends calling each other is real, and layoutFlow removes
	// back edges by DFS, so a cycle is drawable rather than fatal.
	path := writeYAML(t, `
repositories:
  - name: shop-orders
    clone_url: https://forge.example.invalid/acme/shop-orders.git
    project: shop
    uses: [shop-billing]
  - name: shop-billing
    clone_url: https://forge.example.invalid/acme/shop-billing.git
    project: shop
    uses: [shop-orders]
`)

	// When
	_, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil: a cycle between siblings is allowed", err)
	}
}

func TestLoad_rejectsTwoBranchesOfOneRepositoryInAProject(t *testing.T) {
	// Given: a project holding two branches of one repository would search the
	// same file at two commits and answer as one product. AGENTS.md already
	// forbids two cards differing only by branch.
	path := writeYAML(t, `
repositories:
  - name: shop-backend
    clone_url: https://forge.example.invalid/acme/shop-backend.git
    project: shop
    branch: master
  - name: shop-backend-release
    clone_url: https://forge.example.invalid/acme/shop-backend.git
    project: shop
    branch: release-2024.3
`)

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("Load() err = nil, want a refusal for one clone_url twice in a project")
	}
}
