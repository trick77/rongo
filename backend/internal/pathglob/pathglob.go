// Package pathglob is the path glob BACKEND_INDEX_EXCLUDE and a repository's
// `index:` block in repos.yaml are written in. Its own package so the loader
// can refuse a malformed pattern without importing the indexer, which imports
// the loader.
package pathglob

import (
	"fmt"
	"path"
	"strings"
)

// Validate reports the first malformed pattern. It runs at startup so a typo
// fails the boot rather than silently matching nothing — the index would then
// keep the excluded content while the configuration looked right. The error
// names the pattern, not the setting: the caller prefixes that.
func Validate(patterns []string) error {
	for _, pat := range patterns {
		if pat == "" {
			return fmt.Errorf("pattern is empty")
		}
		// Not trimmed away: a YAML list keeps a quoted space, and a pattern
		// that never matches must fail as loudly as one that does not parse.
		if pat != strings.TrimSpace(pat) {
			return fmt.Errorf("pattern %q has leading or trailing whitespace, which no path carries", pat)
		}
		for _, seg := range strings.Split(pat, "/") {
			// Paths are cleaned before matching and never contain these, so a
			// pattern carrying one (a gitignore-style trailing slash, a leading
			// "/" or "./", a doubled slash) could never match anything.
			switch seg {
			case "", ".", "..":
				return fmt.Errorf("pattern %q: segments are matched against a cleaned path, so a leading, trailing or doubled slash and \".\" or \"..\" never match", pat)
			}
			// "**" spans directories only as a whole segment; glued to other
			// text path.Match would quietly read it as "*".
			if strings.Contains(seg, "**") && seg != "**" {
				return fmt.Errorf("pattern %q: \"**\" must be a whole segment, as in docs/**/*.html", pat)
			}
			if _, err := path.Match(seg, ""); err != nil {
				return fmt.Errorf("pattern %q: %w", pat, err)
			}
		}
	}
	return nil
}

// Match matches a pattern against a path, both already split on "/". "**"
// consumes zero or more path segments; every other pattern segment must match
// exactly one path segment via path.Match. Anchored at both ends.
func Match(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if Match(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		// The pattern was validated at startup, so an error here cannot occur.
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
