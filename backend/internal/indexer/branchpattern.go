package indexer

import (
	"fmt"
	"time"

	"github.com/trick77/rongo/internal/repos"
)

// PatternsOf is each repository's branch pattern, for the poller. Like
// RulesOf, it is read from the file at start: repo_state holds the branch
// a pattern resolved to, never the pattern itself.
func PatternsOf(specs []repos.Spec) map[string]*repos.BranchPattern {
	out := map[string]*repos.BranchPattern{}
	for _, s := range specs {
		if s.BranchPattern != nil {
			out[s.Name] = s.BranchPattern
		}
	}
	return out
}

// targetMonth is the release month counted as rolled out at now: the
// current one from the pattern's switch day, the previous one before it.
// As y*12+m-1, so a month back across a year end is one subtraction.
func targetMonth(p *repos.BranchPattern, now time.Time) int {
	m := now.Year()*12 + int(now.Month()) - 1
	if now.Day() < p.SwitchDay() {
		m--
	}
	return m
}

// monthString renders targetMonth's number as 2026-11.
func monthString(m int) string {
	return fmt.Sprintf("%04d-%02d", m/12, m%12+1)
}

// resolveBranch picks the branch a pattern entry indexes at now: of the
// branches the pattern matches whole, the newest month not after the
// target month, and in that month the highest {n}. older reports that the
// chosen month is before the target — a month skipped or the scheme
// renamed — which the caller logs and accepts.
//
// Nothing matching at or before the target is an error, never the default
// branch: indexing master under a release label is a confident wrong
// answer, a recorded error with the old index standing is not.
func resolveBranch(p *repos.BranchPattern, branches []string, now time.Time) (branch string, older bool, err error) {
	target := targetMonth(p, now)
	best, bestMonth, bestN := "", -1, ""
	for _, b := range branches {
		y, mo, n, ok := p.Match(b)
		if !ok {
			continue
		}
		m := y*12 + mo - 1
		if m > target {
			continue
		}
		if m > bestMonth || (m == bestMonth && compareVersionNames(n, bestN) > 0) {
			best, bestMonth, bestN = b, m, n
		}
	}
	if best == "" {
		return "", false, fmt.Errorf("no branch matches %q at or before %s", p.String(), monthString(target))
	}
	return best, bestMonth < target, nil
}
