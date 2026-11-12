package repos

import (
	"slices"
	"strings"
	"testing"
)

const indexedProject = `
projects:
  - name: acme
    repositories:
      - name: acme-service
        clone_url: https://forge.example.invalid/acme/acme-service.git
        index:
          exclude: [docs/drafts/**]
          latest: ["fixtures/seed/[0-9][0-9][0-9][0-9]_*"]
      - name: acme-ui
        clone_url: https://forge.example.invalid/acme/acme-ui.git
`

func TestLoad_readsTheIndexBlock(t *testing.T) {
	// Given: one entry with an index block, one without.
	specs, err := Load(writeYAML(t, indexedProject))

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	got := specs[0].Index
	if !slices.Equal(got.Exclude, []string{"docs/drafts/**"}) {
		t.Errorf("Index.Exclude = %v, want [docs/drafts/**]", got.Exclude)
	}
	if !slices.Equal(got.Latest, []string{"fixtures/seed/[0-9][0-9][0-9][0-9]_*"}) {
		t.Errorf("Index.Latest = %v, want the seed glob", got.Latest)
	}
	if specs[1].Index.Exclude != nil || specs[1].Index.Latest != nil {
		t.Errorf("the entry without a block got %+v", specs[1].Index)
	}
}

func TestLoad_readsTheIndexBlockOnALibrary(t *testing.T) {
	specs, err := Load(writeYAML(t, `
libraries:
  - name: acme-commons
    clone_url: https://forge.example.invalid/acme/commons.git
    index:
      exclude: [old/**]
projects:
  - name: acme
    repositories:
      - name: acme-service
        clone_url: https://forge.example.invalid/acme/acme-service.git
`))
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if !slices.Equal(specs[0].Index.Exclude, []string{"old/**"}) {
		t.Errorf("library Index.Exclude = %v, want [old/**]", specs[0].Index.Exclude)
	}
}

// A glob holding [ cannot sit unquoted in a [..] list; in a block list it can.
func TestLoad_readsAnUnquotedGlobFromABlockList(t *testing.T) {
	specs, err := Load(writeYAML(t, `
projects:
  - name: acme
    repositories:
      - name: acme-service
        clone_url: https://forge.example.invalid/acme/acme-service.git
        index:
          latest:
            - fixtures/seed/[0-9][0-9][0-9][0-9]_*
`))
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if !slices.Equal(specs[0].Index.Latest, []string{"fixtures/seed/[0-9][0-9][0-9][0-9]_*"}) {
		t.Errorf("Index.Latest = %v, want the seed glob", specs[0].Index.Latest)
	}
}

func TestLoad_refusesABadIndexBlock(t *testing.T) {
	cases := []struct {
		name, block, want string
	}{
		{"unclosed bracket in exclude", "          exclude: [\"docs/[drafts/**\"]\n", "index.exclude"},
		{"trailing slash in exclude", "          exclude: [docs/drafts/]\n", "index.exclude"},
		{"empty exclude pattern", "          exclude: [\"\"]\n", "index.exclude"},
		{"padded exclude pattern", "          exclude: [\"docs/drafts/** \"]\n", "whitespace"},
		{"padded latest pattern", "          latest: [\" seed/[0-9]*\"]\n", "whitespace"},
		{"unclosed bracket in latest", "          latest: [\"seed/[0-9\"]\n", "index.latest"},
		// The hint must not suggest a bare *: common/ sorts above 2027_X.
		{"latest ending in **", "          latest: [seed/**]\n", "seed/[0-9]"},
		{"latest naming one directory", "          latest: [seed/current]\n", "no wildcard"},
		{"unknown key", "          newest: [seed/*]\n", "newest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yaml := `
projects:
  - name: acme
    repositories:
      - name: acme-service
        clone_url: https://forge.example.invalid/acme/acme-service.git
        index:
` + tc.block
			_, err := Load(writeYAML(t, yaml))
			if err == nil {
				t.Fatal("Load() err = nil, want the block refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load() err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
