package repos

import "testing"

// enabledByName reads the flattened result back as a map, because nothing past
// Load knows which of the two levels said no — and that is the point.
func enabledByName(t *testing.T, path string) map[string]bool {
	t.Helper()
	specs, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	out := map[string]bool{}
	for _, s := range specs {
		out[s.Name] = s.Enabled
	}
	return out
}

// TestLoad_projectEnabledFalseParksEveryMember: parking a six-repository product
// is one edit, not six.
func TestLoad_projectEnabledFalseParksEveryMember(t *testing.T) {
	// Given
	path := writeYAML(t, `
projects:
  - name: legacy-crm
    enabled: false
    repositories:
      - name: legacy-crm-api
        clone_url: https://forge.example.invalid/acme/legacy-crm-api.git
      - name: legacy-crm-ui
        clone_url: https://forge.example.invalid/acme/legacy-crm-ui.git
  - name: shop
    repositories:
      - name: shop-ui
        clone_url: https://forge.example.invalid/acme/shop-ui.git
`)

	// When
	got := enabledByName(t, path)

	// Then
	if got["legacy-crm-api"] || got["legacy-crm-ui"] {
		t.Errorf("legacy-crm members enabled = %v, want both parked", got)
	}
	if !got["shop-ui"] {
		t.Error("shop-ui enabled = false, want true — another project's flag must not reach it")
	}
}

// TestLoad_aMemberCannotOptOutOfAParkedProject: a parked product is parked
// whole. The reverse would let one repository keep a hidden project half-alive,
// answering questions out of a product nobody can see on the page.
func TestLoad_aMemberCannotOptOutOfAParkedProject(t *testing.T) {
	// Given
	path := writeYAML(t, `
projects:
  - name: legacy-crm
    enabled: false
    repositories:
      - name: legacy-crm-api
        clone_url: https://forge.example.invalid/acme/legacy-crm-api.git
        enabled: true
`)

	// When
	got := enabledByName(t, path)

	// Then
	if got["legacy-crm-api"] {
		t.Error("legacy-crm-api enabled = true, want false — the project's flag wins")
	}
}

// TestLoad_aMemberParksAloneInsideALiveProject: the existing behaviour, kept.
// Parking one half of a product is normal.
func TestLoad_aMemberParksAloneInsideALiveProject(t *testing.T) {
	// Given
	path := writeYAML(t, `
projects:
  - name: shop
    enabled: true
    repositories:
      - name: shop-ui
        clone_url: https://forge.example.invalid/acme/shop-ui.git
      - name: shop-events
        clone_url: https://forge.example.invalid/acme/shop-events.git
        enabled: false
`)

	// When
	got := enabledByName(t, path)

	// Then
	if !got["shop-ui"] {
		t.Error("shop-ui enabled = false, want true")
	}
	if got["shop-events"] {
		t.Error("shop-events enabled = true, want false")
	}
}

// TestLoad_bothOmittedMeansEnabled: the *bool on each level exists for exactly
// this. A plain bool would default to false and silently park the whole corpus.
func TestLoad_bothOmittedMeansEnabled(t *testing.T) {
	// Given
	path := writeYAML(t, `
projects:
  - name: shop
    repositories:
      - name: shop-ui
        clone_url: https://forge.example.invalid/acme/shop-ui.git
`)

	// When
	got := enabledByName(t, path)

	// Then
	if !got["shop-ui"] {
		t.Error("shop-ui enabled = false, want true by default")
	}
}
