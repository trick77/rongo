package edges

import (
	"context"
	"testing"
)

func TestExtractReadsKustomizeImagesAndInlineImageLines(t *testing.T) {
	// Given: a kustomize overlay naming versions two ways — the images
	// transformer, and an image line inside a manifest.
	kustomization := `
resources:
  - ../../base
images:
  - name: registry.example.invalid/acme/shop-backend
    newTag: 2024.3.1
  - name: shop-ui
    newName: registry.example.invalid/acme/shop-ui
    newTag: v5.2.0
  - name: registry.example.invalid/acme/worker
    digest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`
	deployment := `
spec:
  template:
    spec:
      containers:
        - name: api
          image: registry.example.invalid/acme/shop-backend:2024.3.1
        - name: sidecar
          image: "quay.io/acme/envoy:1.29"
        - image: ghcr.io/acme/tool@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
`

	// When
	kt := values(Extract("prod/kustomization.yaml", []byte(kustomization)), KindImage)
	dt := values(Extract("prod/deployment.yml", []byte(deployment)), KindImage)

	// Then
	for _, want := range []string{
		"registry.example.invalid/acme/shop-backend:2024.3.1",
		"registry.example.invalid/acme/shop-ui:v5.2.0",
		"registry.example.invalid/acme/worker@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		if !has(kt, want) {
			t.Errorf("kustomization: missing %q in %v", want, kt)
		}
	}
	for _, want := range []string{
		"registry.example.invalid/acme/shop-backend:2024.3.1",
		"quay.io/acme/envoy:1.29",
		"ghcr.io/acme/tool@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	} {
		if !has(dt, want) {
			t.Errorf("deployment: missing %q in %v", want, dt)
		}
	}
}

func TestExtractSkipsPlaceholdersUntaggedImagesAndOtherYAMLKeys(t *testing.T) {
	// Given: the shapes that are not a deployed version — a placeholder a
	// pipeline fills, an untagged base image, a Helm values pair, and a
	// key that merely ends in "image".
	body := `
image: registry.example.invalid/acme/shop-backend:${VERSION}
image: registry.example.invalid/acme/shop-backend:{{ .Values.tag }}
image: registry.example.invalid/acme/shop-backend
images:
  - name: registry.example.invalid/acme/shop-backend
image:
  repository: registry.example.invalid/acme/shop-backend
  tag: 1.2.3
baseImage: registry.example.invalid/acme/base:1
`

	// When
	got := values(Extract("base/values.yaml", []byte(body)), KindImage)

	// Then
	if len(got) != 0 {
		t.Errorf("extracted %v, want nothing", got)
	}
}

func TestExtractReadsImagesOnlyFromYAML(t *testing.T) {
	// A properties file and a source file keep their own branches: the
	// image rule is the one thing yaml gets, and yaml is the one place it runs.
	if got := values(Extract("app.properties", []byte("image=acme/shop:1.0\n")), KindImage); len(got) != 0 {
		t.Errorf("properties yielded %v", got)
	}
	if got := values(Extract("deploy.go", []byte(`image := "acme/shop:1.0"`)), KindImage); len(got) != 0 {
		t.Errorf("go yielded %v", got)
	}
	if got := values(Extract("prod/app.yaml", []byte("image: acme/shop:1.0\n")), KindProperty); len(got) != 0 {
		t.Errorf("yaml yielded property keys %v", got)
	}
}

func TestImagesAreNeverAnEdge(t *testing.T) {
	// Given: two infrastructure repositories deploying the same image. That
	// is a fact about the image, not a call between them.
	db := edgeDB(t, "shop-infra", "crm-infra")
	seedFileWithTokens(t, db, "shop-infra", "prod/kustomization.yaml", "a", nil,
		[]Token{{Kind: KindImage, Value: "acme/base:1", Line: 1}})
	seedFileWithTokens(t, db, "crm-infra", "prod/kustomization.yaml", "b", nil,
		[]Token{{Kind: KindImage, Value: "acme/base:1", Line: 2}})

	// When
	got, err := Neighbours(context.Background(), db, "shop-infra", "prod/kustomization.yaml")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	holders, err := Holders(context.Background(), db, KindImage, "acme/base:1")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}
	census, err := InRepo(context.Background(), db, "shop-infra", KindImage)
	if err != nil {
		t.Fatalf("InRepo: %v", err)
	}

	// Then
	if len(got) != 0 {
		t.Errorf("an image was crossed: %+v", got)
	}
	if len(holders) != 0 {
		t.Errorf("an image was held: %+v", holders)
	}
	if len(census) != 1 || census[0].Value != "acme/base:1" {
		t.Errorf("census = %+v, want the one image", census)
	}
}
