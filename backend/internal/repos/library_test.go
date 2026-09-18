package repos

import (
	"strings"
	"testing"
)

const twoProductsOneLibrary = `
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
    description: Shared Java utilities.
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        uses: [acme-commons]
  - name: billing
    repositories:
      - name: billing-api
        clone_url: https://forge.example.invalid/acme/billing-api.git
        uses: [acme-commons]
`

func TestLoad_aLibraryIsDeclaredOnceAndUsedFromAnyProject(t *testing.T) {
	// Given a library two products are built on, declared once at the top
	// level and named by a uses edge from each.
	specs, err := Load(writeYAML(t, twoProductsOneLibrary))
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}

	// Then it is one flat entry among the rest, a project of one named after
	// itself, marked as a library, and both edges survive.
	by := map[string]Spec{}
	for _, s := range specs {
		by[s.Name] = s
	}
	lib, ok := by["acme-commons"]
	if !ok {
		t.Fatalf("Load() = %v, want acme-commons among the specs", specNames(specs))
	}
	if !lib.Library || lib.Project != "acme-commons" || !lib.Enabled {
		t.Errorf("acme-commons = %+v, want Library, Project == its own name, enabled", lib)
	}
	if lib.Description != "Shared Java utilities." {
		t.Errorf("Description = %q, want the declared sentence — a library takes every ordinary field", lib.Description)
	}
	for _, r := range []string{"shop-backend", "billing-api"} {
		if got := by[r].Uses; len(got) != 1 || got[0] != "acme-commons" {
			t.Errorf("%s.Uses = %v, want [acme-commons]", r, got)
		}
		if by[r].Library {
			t.Errorf("%s is marked a library and is a project member", r)
		}
	}
}

func TestLoad_aLibraryMayUseAnotherLibrary(t *testing.T) {
	specs, err := Load(writeYAML(t, `
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
  - name: acme-http
    clone_url: https://forge.example.invalid/acme/http.git
    uses: [acme-commons]
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        uses: [acme-http]
`))
	if err != nil {
		t.Fatalf("Load() err = %v, want a library using another library to be fine", err)
	}
	if len(specs) != 3 {
		t.Errorf("Load() = %v, want three entries", specNames(specs))
	}
}

func TestLoad_refusesTheLibraryShapesThatWouldMakeItOneProducts(t *testing.T) {
	// Each of these would quietly turn the shared thing into a member of one
	// product, or index it twice. Refused by name, never dropped.
	cases := map[string]struct{ body, want string }{
		"a library using a project member": {`
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
    uses: [shop-backend]
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
`, "may only use other libraries"},
		"the library's clone_url listed in a project too": {`
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
projects:
  - name: shop
    repositories:
      - name: shop-commons
        clone_url: https://forge.example.invalid/acme/commons.git
`, "declared once"},
		"a project named after a library": {`
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
projects:
  - name: acme-commons
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
`, "duplicate project name"},
		"a repository named after a library": {`
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
projects:
  - name: shop
    repositories:
      - name: acme-commons
        clone_url: https://forge.example.invalid/acme/shop-backend.git
`, "duplicate repository name"},
		"libraries with no project at all": {`
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
`, "names no project"},
		"an unknown key on a library": {`
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
    project: shop
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
`, "project"},
		"a cross-project edge that is not a library": {`
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        uses: [billing-api]
  - name: billing
    repositories:
      - name: billing-api
        clone_url: https://forge.example.invalid/acme/billing-api.git
`, "coupling across projects"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeYAML(t, c.body))
			if err == nil {
				t.Fatalf("Load() err = nil, want a refusal for %s", name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("Load() err = %q, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestLoad_aParkedLibraryIsParkedLikeAnyEntry(t *testing.T) {
	specs, err := Load(writeYAML(t, `
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
    enabled: false
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        uses: [acme-commons]
`))
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	for _, s := range specs {
		if s.Name == "acme-commons" && s.Enabled {
			t.Error("acme-commons is enabled, want parked")
		}
	}
}

func specNames(specs []Spec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}
