package indexer

import (
	"strings"
	"testing"
	"time"

	"github.com/trick77/rongo/internal/repos"
)

func mustPattern(t *testing.T, text string, day int) *repos.BranchPattern {
	t.Helper()
	p, err := repos.ParseBranchPattern(text, day)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local)
}

func TestResolveBranch(t *testing.T) {
	months := []string{"main", "rel/2026-09.1", "rel/2026-10.1", "rel/2026-10.2", "rel/2026-11.1"}
	cases := []struct {
		name     string
		pattern  string
		switchAt int
		branches []string
		now      time.Time
		want     string
		older    bool
		err      string
	}{
		{"day before the switch day keeps last month", "rel/{yyyy}-{mm}.{n}", 25, months, day(2026, 11, 24), "rel/2026-10.2", false, ""},
		{"the switch day itself moves on", "rel/{yyyy}-{mm}.{n}", 25, months, day(2026, 11, 25), "rel/2026-11.1", false, ""},
		{"no switch day: the current month from the 1st", "rel/{yyyy}-{mm}.{n}", 0, months, day(2026, 11, 1), "rel/2026-11.1", false, ""},
		{"a future month is never picked", "rel/{yyyy}-{mm}.{n}", 1,
			[]string{"rel/2026-11.1", "rel/2026-12.1"}, day(2026, 11, 30), "rel/2026-11.1", false, ""},
		{"early January picks December of the year before", "rel/{yyyy}-{mm}.{n}", 10,
			[]string{"rel/2026-11.1", "rel/2026-12.1", "rel/2027-01.1"}, day(2027, 1, 5), "rel/2026-12.1", false, ""},
		{"the 27th before switch day 28 is still last month", "rel/{yyyy}-{mm}.{n}", 28,
			[]string{"rel/2026-02.1", "rel/2026-03.1"}, day(2026, 3, 27), "rel/2026-02.1", false, ""},
		{"a skipped month falls to the older one, flagged", "rel/{yyyy}-{mm}.{n}", 1,
			[]string{"rel/2026-09.1", "rel/2026-11.1"}, day(2026, 10, 15), "rel/2026-09.1", true, ""},
		{"two in one month: the highest n", "rel/{yyyy}-{mm}.{n}", 1,
			[]string{"rel/2026-11.9", "rel/2026-11.10", "rel/2026-11.2"}, day(2026, 11, 15), "rel/2026-11.10", false, ""},
		{"a suffix after n does not match", "rel/{yyyy}-{mm}.{n}", 1,
			[]string{"rel/2026-10.1", "rel/2026-11.1_x"}, day(2026, 11, 15), "rel/2026-10.1", true, ""},
		{"a prefix line does not match", "rel/{yyyy}-{mm}.{n}", 1,
			[]string{"rel/2026-10.1", "rel/next_2026-11.1"}, day(2026, 11, 15), "rel/2026-10.1", true, ""},
		{"two-digit year", "release-{yy}.{mm}", 1,
			[]string{"release-26.10", "release-26.11", "release-2026.11"}, day(2026, 11, 15), "release-26.11", false, ""},
		{"four-digit year without n", "release-{yyyy}.{mm}", 1,
			[]string{"release-26.11", "release-2026.10"}, day(2026, 11, 15), "release-2026.10", true, ""},
		{"nothing matches", "rel/{yyyy}-{mm}.{n}", 1, []string{"main", "develop"}, day(2026, 11, 15), "", false, "matches"},
		{"only future months", "rel/{yyyy}-{mm}.{n}", 1,
			[]string{"rel/2026-12.1", "rel/2027-01.1"}, day(2026, 11, 15), "", false, "2026-11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			p := mustPattern(t, tc.pattern, tc.switchAt)

			// When
			got, older, err := resolveBranch(p, tc.branches, tc.now)

			// Then
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one containing %q", err, tc.err)
				}
				if !strings.Contains(err.Error(), tc.pattern) {
					t.Errorf("err = %v, want it to name the pattern", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want || older != tc.older {
				t.Errorf("resolveBranch = %q, older %v; want %q, older %v", got, older, tc.want, tc.older)
			}
		})
	}
}
