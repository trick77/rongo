package repos

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// BranchPattern names an entry's branch by release month rather than by a
// fixed name: "rel/{yyyy}-{mm}.{n}". The poller picks, at every poll, the
// newest matching branch of the month counted as rolled out.
//
// Placeholders are {yyyy} or {yy} (20yy), {mm} (01..12) and {n} (digits);
// everything else is literal, and the whole branch name must match. A
// glob would let rel/next_2026-11.1 or rel/2026-11.1_x claim a month they
// are not the release of.
type BranchPattern struct {
	text      string
	switchDay int
	re        *regexp.Regexp
	// Submatch indices of the placeholders; n is 0 when the pattern has none.
	year, month, n int
	// shortYear is true for {yy}: the year is 2000 plus the two digits.
	shortYear bool
}

// placeholderRe finds every {...}; what is between the braces decides
// whether it is a placeholder or a typo.
var placeholderRe = regexp.MustCompile(`\{[^{}]*\}`)

// placeholderExpr is each placeholder's regular expression. {mm} admits the
// twelve months only, so a 13 is no match rather than a bad month.
var placeholderExpr = map[string]string{
	"{yyyy}": `([0-9]{4})`,
	"{yy}":   `([0-9]{2})`,
	"{mm}":   `(0[1-9]|1[0-2])`,
	"{n}":    `([0-9]+)`,
}

// ParseBranchPattern compiles a branch_pattern. switchDay is the day of the
// month from which the current month counts as rolled out; 0 (absent)
// means the 1st. Its range is checked by the caller, which owns the YAML.
func ParseBranchPattern(text string, switchDay int) (*BranchPattern, error) {
	if switchDay == 0 {
		switchDay = 1
	}
	p := &BranchPattern{text: text, switchDay: switchDay}
	var expr strings.Builder
	expr.WriteString("^")
	seen := map[string]bool{}
	last, group := 0, 0
	for _, loc := range placeholderRe.FindAllStringIndex(text, -1) {
		literal, ph := text[last:loc[0]], text[loc[0]:loc[1]]
		if strings.ContainsAny(literal, "{}") {
			return nil, fmt.Errorf("branch_pattern %q has a brace outside a placeholder", text)
		}
		expr.WriteString(regexp.QuoteMeta(literal))
		re, ok := placeholderExpr[ph]
		if !ok {
			return nil, fmt.Errorf("branch_pattern %q: unknown placeholder %s, want {yyyy}, {yy}, {mm} or {n}", text, ph)
		}
		if seen[ph] {
			return nil, fmt.Errorf("branch_pattern %q uses %s twice", text, ph)
		}
		seen[ph] = true
		group++
		switch ph {
		case "{yyyy}":
			p.year = group
		case "{yy}":
			p.year, p.shortYear = group, true
		case "{mm}":
			p.month = group
		case "{n}":
			p.n = group
		}
		expr.WriteString(re)
		last = loc[1]
	}
	if strings.ContainsAny(text[last:], "{}") {
		return nil, fmt.Errorf("branch_pattern %q has a brace outside a placeholder", text)
	}
	expr.WriteString(regexp.QuoteMeta(text[last:]))
	expr.WriteString("$")
	switch {
	case seen["{yy}"] && seen["{yyyy}"]:
		return nil, fmt.Errorf("branch_pattern %q uses both {yy} and {yyyy}, want one year", text)
	case p.year == 0:
		return nil, fmt.Errorf("branch_pattern %q has no year, want {yyyy} or {yy}", text)
	case p.month == 0:
		return nil, fmt.Errorf("branch_pattern %q has no month, want {mm}", text)
	}
	p.re = regexp.MustCompile(expr.String())
	return p, nil
}

// String is the pattern as repos.yaml writes it.
func (p *BranchPattern) String() string { return p.text }

// SwitchDay is the first day of the month on which that month's branch is
// the one indexed; before it, the previous month's is.
func (p *BranchPattern) SwitchDay() int { return p.switchDay }

// Match reports the release month a branch name stands for, and its {n}
// digits ("" for a pattern without {n}). ok is false for a name the
// pattern does not match whole.
func (p *BranchPattern) Match(branch string) (year, month int, n string, ok bool) {
	m := p.re.FindStringSubmatch(branch)
	if m == nil {
		return 0, 0, "", false
	}
	// The groups are digits of a fixed width, so neither conversion can fail.
	year, _ = strconv.Atoi(m[p.year])
	if p.shortYear {
		year += 2000
	}
	month, _ = strconv.Atoi(m[p.month])
	if p.n > 0 {
		n = m[p.n]
	}
	return year, month, n, true
}

// loadBranchPattern validates an entry's branch_pattern and
// branch_switch_day. nil, nil for an entry naming its branch the usual way.
func loadBranchPattern(r rawSpec) (*BranchPattern, error) {
	text := strings.TrimSpace(r.BranchPattern)
	if text == "" {
		if r.BranchSwitchDay != nil {
			return nil, fmt.Errorf("%s: branch_switch_day needs a branch_pattern to go with it", r.Name)
		}
		return nil, nil
	}
	// Either one would be silently ignored: the pattern decides the branch.
	if strings.TrimSpace(r.Branch) != "" {
		return nil, fmt.Errorf("%s: branch and branch_pattern are exclusive — name the branch or the pattern, not both", r.Name)
	}
	day := 0
	if r.BranchSwitchDay != nil {
		// 28 at most: a later day would not occur every month, and that
		// month would never switch.
		if day = *r.BranchSwitchDay; day < 1 || day > 28 {
			return nil, fmt.Errorf("%s: branch_switch_day %d is outside 1..28", r.Name, day)
		}
	}
	p, err := ParseBranchPattern(text, day)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.Name, err)
	}
	return p, nil
}
