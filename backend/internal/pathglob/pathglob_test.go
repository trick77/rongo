package pathglob

import (
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"docs/plans/**", "docs/plans/x.md", true},
		{"docs/plans/**", "services/x/docs/plans/x.md", false},
		{"**/docs/plans/**", "services/x/docs/plans/x.md", true},
		{"docs/plans/**", "docs/plans-notes.md", false},
		{"seed/[0-9][0-9][0-9][0-9]_*", "seed/2027_V9005", true},
		{"seed/[0-9][0-9][0-9][0-9]_*", "seed/inyear_V1234", false},
	}
	for _, tc := range cases {
		if got := Match(strings.Split(tc.pat, "/"), strings.Split(tc.path, "/")); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pat, tc.path, got, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := Validate([]string{"docs/plans/**", "**/*.html"}); err != nil {
		t.Errorf("Validate(valid) err = %v, want nil", err)
	}
	for _, pat := range []string{"", "docs/[plans/**", "docs/", "/docs", "./docs", "docs//x", "docs/../x", "../docs/**", "docs/x**", "docs/**.html", " docs/**", "docs/** "} {
		err := Validate([]string{pat})
		if err == nil {
			t.Errorf("Validate(%q) err = nil, want an error", pat)
			continue
		}
		// The caller names the setting: the same glob is an exclusion in one
		// place and a version directory in another.
		if strings.Contains(err.Error(), "exclusion") {
			t.Errorf("Validate(%q) err = %v, want it worded for any caller", pat, err)
		}
	}
}
