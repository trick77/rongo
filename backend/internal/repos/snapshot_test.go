package repos

import (
	"strings"
	"testing"
)

// TestLoad_snapshotEntryNeedsNoCloneURL: a snapshot is a directory the operator
// extracted by hand. There is no remote, so clone_url is not merely optional —
// requiring it would be requiring a URL that names nothing.
func TestLoad_snapshotEntryNeedsNoCloneURL(t *testing.T) {
	// Given
	path := writeYAML(t, `
projects:
  - name: acme-core
    repositories:
      - name: acme-core
        snapshot: true
        part: backend
        description: Vendor drop, release 4.2.
`)

	// When
	specs, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	if !specs[0].Snapshot {
		t.Error("specs[0].Snapshot = false, want true")
	}
	if specs[0].CloneURL != "" {
		t.Errorf("specs[0].CloneURL = %q, want empty", specs[0].CloneURL)
	}
	if !specs[0].Enabled {
		t.Error("specs[0].Enabled = false, want true by default")
	}
}

// TestLoad_snapshotRefusesRemoteFields: each of the three is refused BY NAME.
// A snapshot has no remote to authenticate to and none to resolve a default
// branch from, so any of them is a contradiction — and a contradiction that
// parsed would leave the entry meaning something the reader did not write.
func TestLoad_snapshotRefusesRemoteFields(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"clone_url", "        clone_url: https://forge.example.invalid/acme/x.git"},
		{"branch", "        branch: master"},
		{"token_env", "        token_env: BACKEND_FORGE_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			path := writeYAML(t, `
projects:
  - name: acme-core
    repositories:
      - name: acme-core
        snapshot: true
`+tc.field+"\n")

			// When
			_, err := Load(path)

			// Then
			if err == nil {
				t.Fatal("Load() err = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("Load() err = %v, want it to name %q", err, tc.name)
			}
			if !strings.Contains(err.Error(), "snapshot") {
				t.Errorf("Load() err = %v, want it to name snapshot", err)
			}
		})
	}
}

// TestLoad_twoSnapshotsInOneProject: the duplicate-clone_url rule keys on the
// URL, and two snapshots share the empty one. Without an exemption, a product
// built from two drops would be refused as "two branches of one repository".
func TestLoad_twoSnapshotsInOneProject(t *testing.T) {
	// Given
	path := writeYAML(t, `
projects:
  - name: acme-shop
    repositories:
      - name: acme-shop-ui
        snapshot: true
        uses: [acme-shop-backend]
      - name: acme-shop-backend
        snapshot: true
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
	for _, s := range specs {
		if !s.Snapshot {
			t.Errorf("%s: Snapshot = false, want true", s.Name)
		}
	}
}

// TestLoad_twoRemotesInOneProjectStillRefused: the exemption above is for the
// EMPTY url only. Two entries naming one remote are still one repository twice.
func TestLoad_twoRemotesInOneProjectStillRefused(t *testing.T) {
	// Given
	path := writeYAML(t, `
projects:
  - name: acme-shop
    repositories:
      - name: acme-shop-a
        clone_url: https://forge.example.invalid/acme/shop.git
      - name: acme-shop-b
        clone_url: https://forge.example.invalid/acme/shop.git
        branch: release-2024.3
`)

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("Load() err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "same clone_url") {
		t.Errorf("Load() err = %v, want it to name the duplicate clone_url", err)
	}
}

// TestLoad_nonSnapshotStillRequiresCloneURL: the escape hatch stays shut for
// everything else. An entry that simply forgot the URL must still be told so,
// not quietly turned into a snapshot of a directory nobody extracted.
func TestLoad_nonSnapshotStillRequiresCloneURL(t *testing.T) {
	// Given
	path := writeYAML(t, `
projects:
  - name: acme-core
    repositories:
      - name: acme-core
        part: backend
`)

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("Load() err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "clone_url is required") {
		t.Errorf("Load() err = %v, want it to say clone_url is required", err)
	}
}
