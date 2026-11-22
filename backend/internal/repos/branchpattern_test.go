package repos

import (
	"strings"
	"testing"
)

// TestLoad_acceptsABranchPattern: the pattern is compiled at load and carries
// its switch day; Branch stays empty, because the poller resolves it.
func TestLoad_acceptsABranchPattern(t *testing.T) {
	// Given
	path := writeYAML(t, `
libraries:
  - name: letters-lib
    clone_url: https://git.example.com/acme/letters-lib.git
    branch_pattern: "lib/{yy}{mm}"
projects:
  - name: letters
    repositories:
      - name: letters
        clone_url: https://git.example.com/acme/letters.git
        branch_pattern: "rel/{yyyy}-{mm}.{n}"
        branch_switch_day: 25
`)

	// When
	specs, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	lib, app := specs[0], specs[1]
	if lib.BranchPattern == nil || lib.BranchPattern.String() != "lib/{yy}{mm}" || lib.BranchPattern.SwitchDay() != 1 {
		t.Errorf("library pattern = %+v, want lib/{yy}{mm} switching on the 1st", lib.BranchPattern)
	}
	if app.BranchPattern == nil || app.BranchPattern.String() != "rel/{yyyy}-{mm}.{n}" || app.BranchPattern.SwitchDay() != 25 {
		t.Errorf("pattern = %+v, want rel/{yyyy}-{mm}.{n} switching on the 25th", app.BranchPattern)
	}
	if app.Branch != "" {
		t.Errorf("Branch = %q, want empty: the poller resolves it", app.Branch)
	}
}

// TestLoad_aPlainEntryHasNoBranchPattern: nil is what tells the poller to
// take the configured or the default branch.
func TestLoad_aPlainEntryHasNoBranchPattern(t *testing.T) {
	path := writeYAML(t, `
projects:
  - name: letters
    repositories:
      - name: letters
        clone_url: https://git.example.com/acme/letters.git
`)
	specs, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if specs[0].BranchPattern != nil {
		t.Errorf("BranchPattern = %+v, want nil", specs[0].BranchPattern)
	}
}

func TestLoad_refusesAMalformedBranchPattern(t *testing.T) {
	cases := []struct {
		name   string
		fields string
		want   string
	}{
		{"with branch", "branch: main\n        branch_pattern: \"rel/{yyyy}-{mm}\"", "branch"},
		{"with snapshot", "snapshot: true\n        branch_pattern: \"rel/{yyyy}-{mm}\"", "branch_pattern"},
		{"day with snapshot", "snapshot: true\n        branch_switch_day: 25", "cannot be combined with branch_switch_day"},
		{"no year", `branch_pattern: "rel/{mm}.{n}"`, "{yyyy}"},
		{"no month", `branch_pattern: "rel/{yyyy}.{n}"`, "{mm}"},
		{"both years", `branch_pattern: "rel/{yy}-{yyyy}-{mm}"`, "{yy}"},
		{"placeholder twice", `branch_pattern: "rel/{yyyy}-{mm}-{mm}"`, "twice"},
		{"n twice", `branch_pattern: "rel/{yyyy}-{mm}.{n}.{n}"`, "twice"},
		{"unknown placeholder", `branch_pattern: "rel/{yyyy}-{mm}.{dd}"`, "{dd}"},
		{"stray brace", `branch_pattern: "rel/{yyyy}-{mm}.{n"`, "brace"},
		{"day without pattern", "branch_switch_day: 25", "branch_switch_day"},
		{"day zero", "branch_pattern: \"rel/{yyyy}-{mm}\"\n        branch_switch_day: 0", "1..28"},
		{"day too late", "branch_pattern: \"rel/{yyyy}-{mm}\"\n        branch_switch_day: 29", "1..28"},
		{"day negative", "branch_pattern: \"rel/{yyyy}-{mm}\"\n        branch_switch_day: -1", "1..28"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			url := "        clone_url: https://git.example.com/acme/letters.git\n"
			if strings.Contains(tc.fields, "snapshot") {
				url = ""
			}
			path := writeYAML(t, `
projects:
  - name: letters
    repositories:
      - name: letters
`+url+"        "+tc.fields+"\n")

			// When
			_, err := Load(path)

			// Then
			if err == nil {
				t.Fatal("Load() err = nil, want the entry refused")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "letters") {
				t.Errorf("err = %v, want it to name the entry and %q", err, tc.want)
			}
		})
	}
}

func TestBranchPattern_matchIsAnchoredAndLiteral(t *testing.T) {
	p, err := ParseBranchPattern("rel/{yyyy}-{mm}.{n}", 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		branch      string
		ok          bool
		year, month int
		n           string
	}{
		{"rel/2026-11.1", true, 2026, 11, "1"},
		{"rel/2026-01.10", true, 2026, 1, "10"},
		{"rel/2026-11.1_x", false, 0, 0, ""},
		{"rel/next_2026-11.1", false, 0, 0, ""},
		{"xrel/2026-11.1", false, 0, 0, ""},
		{"rel/2026-13.1", false, 0, 0, ""},
		{"rel/2026-00.1", false, 0, 0, ""},
		{"rel/2026-11x1", false, 0, 0, ""}, // the dot is literal, not "any character"
		{"rel/2026-11.", false, 0, 0, ""},
		{"rel/26-11.1", false, 0, 0, ""},
	}
	for _, tc := range cases {
		y, m, n, ok := p.Match(tc.branch)
		if ok != tc.ok || y != tc.year || m != tc.month || n != tc.n {
			t.Errorf("Match(%q) = %d, %d, %q, %v; want %d, %d, %q, %v",
				tc.branch, y, m, n, ok, tc.year, tc.month, tc.n, tc.ok)
		}
	}
}

func TestBranchPattern_twoDigitYearIsThisCentury(t *testing.T) {
	p, err := ParseBranchPattern("{mm}/{yy}", 1)
	if err != nil {
		t.Fatal(err)
	}
	y, m, n, ok := p.Match("03/27")
	if !ok || y != 2027 || m != 3 || n != "" {
		t.Errorf("Match = %d, %d, %q, %v; want 2027, 3, \"\", true", y, m, n, ok)
	}
	if _, _, _, ok := p.Match("03/2027"); ok {
		t.Error("Match(03/2027) = true, want {yy} to take two digits only")
	}
}
