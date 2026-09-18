package repos

import (
	"strings"
	"testing"
)

func TestLoad_readsTheDeclaredImage(t *testing.T) {
	specs, err := Load(writeYAML(t, `
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        image: " registry.example.invalid/acme/shop-backend "
`))
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if got := specs[0].Image; got != "registry.example.invalid/acme/shop-backend" {
		t.Errorf("Image = %q, want the trimmed name", got)
	}
}

func TestLoad_refusesAnImageCarryingAVersionOrRepeatedInAProject(t *testing.T) {
	// Given: image is the NAME the infrastructure repository deploys, which
	// the census pairs with a tag per stage. A tag or digest in the
	// declaration would pin one version for every stage, and two members
	// claiming one image would make the range's repository a coin toss.
	cases := map[string]string{
		"a tag": `
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        image: registry.example.invalid/acme/shop-backend:1.2.3
`,
		"a digest": `
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        image: registry.example.invalid/acme/shop-backend@sha256:0123abcd
`,
		"two members": `
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        image: registry.example.invalid/acme/shop
      - name: shop-ui
        clone_url: https://forge.example.invalid/acme/shop-ui.git
        image: registry.example.invalid/acme/shop
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeYAML(t, body))
			if err == nil {
				t.Fatalf("Load() err = nil, want a refusal for %s", name)
			}
			if !strings.Contains(err.Error(), "image") {
				t.Errorf("Load() err = %v, want it to name the image", err)
			}
		})
	}
}

func TestLoad_aRegistryPortIsNotAVersion(t *testing.T) {
	specs, err := Load(writeYAML(t, `
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        image: registry.example.invalid:5000/acme/shop-backend
`))
	if err != nil {
		t.Fatalf("Load() err = %v, want a registry port accepted", err)
	}
	if specs[0].Image != "registry.example.invalid:5000/acme/shop-backend" {
		t.Errorf("Image = %q", specs[0].Image)
	}
}

func TestLoad_allowsOneImageInTwoProjects(t *testing.T) {
	// A registry path is only unique inside the product that deploys it;
	// two products may well ship a library image under one name.
	_, err := Load(writeYAML(t, `
projects:
  - name: shop
    repositories:
      - name: shop-backend
        clone_url: https://forge.example.invalid/acme/shop-backend.git
        image: registry.example.invalid/acme/backend
  - name: crm
    repositories:
      - name: crm-backend
        clone_url: https://forge.example.invalid/acme/crm-backend.git
        image: registry.example.invalid/acme/backend
`))
	if err != nil {
		t.Fatalf("Load() err = %v, want one image in two projects accepted", err)
	}
}
