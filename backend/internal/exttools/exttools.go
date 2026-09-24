// Package exttools locates the external binaries rongo shells out to and
// verifies they are the ones it actually needs.
//
// The dev environment runs without a container, so the binaries come from the
// developer's machine and cannot be assumed correct. ctags in particular: macOS
// ships Apple's BSD ctags at /usr/bin/ctags, which rejects long options. Using
// it would yield an empty symbol index instead of an error, so rongo refuses to
// start rather than indexing silently wrong.
package exttools

import (
	"fmt"
	"os/exec"
	"strings"
)

// Paths holds the resolved absolute paths of the external tools.
type Paths struct {
	Git   string
	Rg    string
	Ctags string
}

// Resolve finds every required binary and validates ctags. It returns the
// first problem it finds, phrased so the fix is obvious from the message.
func Resolve() (Paths, error) {
	var p Paths
	var err error

	if p.Git, err = exec.LookPath("git"); err != nil {
		return Paths{}, fmt.Errorf("git not found in PATH: %w", err)
	}
	if p.Rg, err = exec.LookPath("rg"); err != nil {
		return Paths{}, fmt.Errorf("ripgrep (rg) not found in PATH: %w", err)
	}
	if p.Ctags, err = exec.LookPath("ctags"); err != nil {
		return Paths{}, fmt.Errorf("ctags not found in PATH (install universal-ctags): %w", err)
	}
	if err := verifyUniversalCtags(p.Ctags); err != nil {
		return Paths{}, err
	}
	return p, nil
}

// verifyUniversalCtags checks the banner rather than trusting the filename.
func verifyUniversalCtags(path string) error {
	out, err := exec.Command(path, "--version").CombinedOutput()
	banner := firstLine(string(out))
	if err != nil {
		return fmt.Errorf(
			"%s --version failed (%q); macOS ships BSD ctags at /usr/bin/ctags — install universal-ctags (brew install universal-ctags)",
			path, banner)
	}
	if !strings.Contains(string(out), "Universal Ctags") {
		return fmt.Errorf(
			"%s is not universal-ctags (reports %q); install universal-ctags (brew install universal-ctags) and make sure it precedes /usr/bin on PATH",
			path, banner)
	}
	// The banner is not enough: every extraction asks for
	// --output-format=json, and a universal-ctags built without the json
	// feature fails on every file. The caller falls back to line windows, so
	// the symbol index would end up empty with nothing at startup saying so.
	features, err := exec.Command(path, "--list-features").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s --list-features failed (%q); install universal-ctags (brew install universal-ctags)",
			path, firstLine(string(features)))
	}
	if !hasFeature(string(features), "json") {
		return fmt.Errorf(
			"%s is universal-ctags built without the json feature, which rongo needs for --output-format=json; install a build with json (brew install universal-ctags)",
			path)
	}
	return nil
}

// hasFeature reads `ctags --list-features`, one feature per line with its
// name first, and reports whether name is among them.
func hasFeature(listing, name string) bool {
	for _, line := range strings.Split(listing, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == name {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
